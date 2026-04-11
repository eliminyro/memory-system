package state

import (
	"encoding/json"
	"log/slog"
	"os"
	"time"
)

type State struct {
	path string
	data stateData
}

type stateData struct {
	Sessions map[string]time.Time `json:"sessions"`
}

func New(path string) *State {
	s := &State{
		path: path,
		data: stateData{Sessions: make(map[string]time.Time)},
	}
	s.load()
	return s
}

func (s *State) AlreadyCaptured(sessionID string) bool {
	_, ok := s.data.Sessions[sessionID]
	return ok
}

func (s *State) MarkCaptured(sessionID string) error {
	s.data.Sessions[sessionID] = time.Now()
	return s.save()
}

func (s *State) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(data, &s.data); err != nil {
		slog.Warn("corrupt state file, resetting", "path", s.path, "error", err)
	}
	if s.data.Sessions == nil {
		s.data.Sessions = make(map[string]time.Time)
	}

	// Prune entries older than 90 days
	cutoff := time.Now().AddDate(0, 0, -90)
	for id, t := range s.data.Sessions {
		if t.Before(cutoff) {
			delete(s.data.Sessions, id)
		}
	}
}

func (s *State) save() error {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}
