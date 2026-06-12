package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

func Upload(ctx context.Context, opts Options) Result {
	state := opts.Store.Load()
	applyRuntimeState(&state, opts.Settings, opts.Creds)

	if !state.Enabled || !state.Managed {
		state.LastError = ""
		_ = opts.Store.Save(state)
		return Result{State: state}
	}
	if opts.Creds == nil || opts.Creds.Token == "" || opts.Creds.IsExpired() {
		state.LastError = "run beacon login before endpoint ingest upload"
		_ = opts.Store.Save(state)
		return Result{State: state}
	}
	if opts.Source == nil {
		state.LastError = "endpoint ingest source is not configured"
		_ = opts.Store.Save(state)
		return Result{State: state}
	}

	client := opts.Client
	if client.URL == "" || client.HTTPClient == nil {
		client = NewClient(opts.Settings.IngestURL, opts.HTTPClient)
	}

	batches, err := opts.Source.Batches(state, DefaultBatchEvents, DefaultBatchBytes)
	if err != nil {
		state.LastError = err.Error()
		_ = opts.Store.Save(state)
		return Result{State: state}
	}
	state.LastError = ""

	uploaded := false
	for _, batch := range batches {
		if len(batch.Events) == 0 {
			state.RejectedCount += batch.Rejected
			state.FileOffsets[batch.Cursor.LogPath] = batch.Cursor.Offset
			continue
		}
		response, err := client.UploadBatch(ctx, opts.Creds.Token, uploadRequest{
			UploadID: uploadID(state.SourceID, batch.Cursor.LogPath, batch.Cursor.Offset),
			Source:   uploadSourceFromMetadata(opts.Source.Metadata()),
			Cursor:   batch.Cursor,
			Events:   batch.Events,
		})
		if err != nil {
			state.LastError = err.Error()
			break
		}
		now := time.Now().UTC().Format(time.RFC3339)
		state.AcceptedCount += response.Accepted
		state.RejectedCount += batch.Rejected + response.Rejected
		state.LastUploadAt = now
		state.LastEventAt = now
		state.LastCursor = batch.Cursor
		state.FileOffsets[batch.Cursor.LogPath] = batch.Cursor.Offset
		state.LastError = ""
		uploaded = true
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_ = opts.Store.Save(state)
	return Result{State: state, Uploaded: uploaded}
}

func uploadSourceFromMetadata(metadata SourceMetadata) uploadSource {
	return uploadSource{
		SourceID:         metadata.SourceID,
		Hostname:         metadata.Hostname,
		EndpointMode:     metadata.EndpointMode,
		LogPath:          metadata.LogPath,
		ContentRetention: metadata.ContentRetention,
		ManagedMode:      metadata.ManagedMode,
	}
}

func uploadID(sourceID, path string, offset int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", sourceID, path, offset)))
	return hex.EncodeToString(sum[:])
}
