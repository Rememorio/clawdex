package cron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type storeFile struct {
	Jobs []Job `json:"jobs"`
}

func loadJobs(path string) ([]Job, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, err
	}
	return sf.Jobs, nil
}

func saveJobs(path string, jobs []Job) error {
	data, err := json.MarshalIndent(storeFile{Jobs: jobs}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".jobs-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename cron store: %w", err)
	}
	return nil
}
