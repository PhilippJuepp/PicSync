package api

import (
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
    "path/filepath"
    "strconv"
    "time"

    "backend/internal/db"
    "backend/internal/storage"

    "github.com/google/uuid"
)

// POST /upload/init
func UploadInitHandler(pg *db.Postgres) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        userID := r.Context().Value(CtxUserID).(int64)

        var req struct {
            Filename string    `json:"filename"`
            Size     int64     `json:"size"`
            Mime     string    `json:"mime"`
            TakenAt  time.Time `json:"taken_at"`
            Hash     string    `json:"hash"`
        }

        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            http.Error(w, "invalid request", http.StatusBadRequest)
            return
        }

        assetID, exists, err := pg.AssetExistsByHash(userID, req.Hash)
        if err != nil {
            http.Error(w, "db error", 500)
            return
        }

        if exists {
            json.NewEncoder(w).Encode(map[string]interface{}{
                "status": "exists",
                "asset_id": assetID,
            })
            return
        }


        uploadID := uuid.New().String()
        tempPath := filepath.Join("/tmp", "picsync_upload_"+uploadID)

        var mimePtr *string
        if req.Mime != "" {
            mimePtr = &req.Mime
        }

        var takenAtPtr *time.Time
        if !req.TakenAt.IsZero() {
            takenAtPtr = &req.TakenAt
        }

        if err := pg.CreateUpload(
            uploadID,
            userID,
            req.Filename,
            req.Size,
            tempPath,
            mimePtr,
            takenAtPtr,
            req.Hash,
        ); err != nil {

            fmt.Println("### CreateUpload ERROR:", err)
            http.Error(w, "db error", http.StatusInternalServerError)
            return
        }

        resp := map[string]interface{}{
            "upload_id": uploadID,
            "offset":    0,
        }
        json.NewEncoder(w).Encode(resp)
    }
}

// POST /upload/chunk
func UploadChunkHandler(pg *db.Postgres) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        uploadID := r.URL.Query().Get("id")
        if uploadID == "" {
            http.Error(w, "missing upload id", http.StatusBadRequest)
            return
        }

        up, err := pg.GetUpload(uploadID)
        if err != nil {
            http.Error(w, "upload not found", http.StatusNotFound)
            return
        }

        offsetStr := r.URL.Query().Get("offset")
        offset, err := strconv.ParseInt(offsetStr, 10, 64)
        if err != nil {
            http.Error(w, "invalid offset", 400)
            return
        }

        f, err := os.OpenFile(
            up.TempPath,
            os.O_CREATE|os.O_WRONLY,
            0644,
        )
        if err != nil {
            http.Error(w, "failed to open temp file", 500)
            return
        }
        defer f.Close()

        data, err := io.ReadAll(r.Body)
        if err != nil {
            http.Error(w, "failed to read chunk", 500)
            return
        }

        n, err := f.WriteAt(data, offset)
        if err == nil {
            up.UploadedOffset = max(up.UploadedOffset, offset + int64(n))
            pg.UpdateUploadOffset(up.ID, up.UploadedOffset)
        }
        if err != nil {
            http.Error(w, "failed to write chunk", 500)
            return
        }

        json.NewEncoder(w).Encode(map[string]interface{}{
            "received": n,
            "offset":   offset + int64(n),
        })
    }
}

// POST /upload/complete
func UploadCompleteHandler(pg *db.Postgres, store storage.Storage) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        userID := r.Context().Value(CtxUserID).(int64)
        uploadID := r.URL.Query().Get("id")
        if uploadID == "" {
            http.Error(w, "missing upload id", http.StatusBadRequest)
            return
        }

        up, err := pg.GetUpload(uploadID)
        if err != nil {
            http.Error(w, "upload not found", http.StatusNotFound)
            return
        }

        info, err := os.Stat(up.TempPath)
        if err != nil || info.Size() != up.TotalSize {
            http.Error(w, "upload incomplete", http.StatusBadRequest)
            return
        }

        storageKey := fmt.Sprintf("%d/%s/original", userID, uuid.New().String())

        f, err := os.Open(up.TempPath)
        if err != nil {
            http.Error(w, "failed to open temp file", http.StatusInternalServerError)
            return
        }
        defer f.Close()

        fileInfo, _ := f.Stat()
        if err := store.Put(r.Context(), storageKey, f, fileInfo.Size()); err != nil {
            http.Error(w, "failed to store file", http.StatusInternalServerError)
            return
        }

        mime := ""
        if up.Mime != nil {
            mime = *up.Mime
        }

        hash := ""
        if up.Hash != nil {
            hash = *up.Hash
        }

        _, err = pg.CreateAsset(
            userID,
            up.Filename,
            "",
            up.TotalSize,
            mime,
            hash,
            up.TakenAt,
            storageKey,
        )

        if err != nil {
            http.Error(w, "failed to save asset", http.StatusInternalServerError)
            return
        }

        pg.CompleteUpload(uploadID)
        os.Remove(up.TempPath)

        w.WriteHeader(http.StatusOK)
        json.NewEncoder(w).Encode(map[string]string{"status": "completed"})
    }
}