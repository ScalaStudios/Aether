package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
)

var Names = []string{"maildrop", "incoming", "active", "deferred", "hold"}

var Actions = []string{"retry", "hold", "release", "delete"}

const MaxLimit = 1000

var idPattern = regexp.MustCompile(`^[0-9A-Za-z]{5,32}$`)

func ValidID(id string) bool {
	return idPattern.MatchString(id)
}

type Recipient struct {
	Address     string `json:"address"`
	DelayReason string `json:"delayReason,omitempty"`
}

type Message struct {
	ID          string      `json:"id"`
	Queue       string      `json:"queue"`
	ArrivalTime time.Time   `json:"arrivalTime"`
	Size        int64       `json:"size"`
	Sender      string      `json:"sender"`
	Recipients  []Recipient `json:"recipients"`
	NextAttempt *time.Time  `json:"nextAttempt,omitempty"`
}

type Filter struct {
	Sender          string `json:"sender,omitempty"`
	RecipientDomain string `json:"recipientDomain,omitempty"`
	Error           string `json:"error,omitempty"`
	Queue           string `json:"queue,omitempty"`
}

func (f Filter) Empty() bool {
	return f == Filter{}
}

func (f Filter) Validate() error {
	switch {
	case f.Queue != "" && !slices.Contains(Names, f.Queue):
		return fmt.Errorf("The queue must be one of %s.", strings.Join(Names, ", "))
	case len(f.Sender) > 320 || len(f.RecipientDomain) > 255 || len(f.Error) > 500:
		return errors.New("A filter value is too long.")
	}
	return nil
}

func (f Filter) Match(m *Message) bool {
	if f.Queue != "" && m.Queue != f.Queue {
		return false
	}
	if f.Sender != "" && !strings.Contains(strings.ToLower(m.Sender), strings.ToLower(f.Sender)) {
		return false
	}
	if f.RecipientDomain == "" && f.Error == "" {
		return true
	}
	domain := strings.ToLower(strings.TrimPrefix(f.RecipientDomain, "@"))
	reason := strings.ToLower(f.Error)
	for _, r := range m.Recipients {
		_, rcptDomain, _ := strings.Cut(r.Address, "@")
		if (domain == "" || strings.EqualFold(rcptDomain, domain)) && (reason == "" || strings.Contains(strings.ToLower(r.DelayReason), reason)) {
			return true
		}
	}
	return false
}

type Summary struct {
	Total         int            `json:"total"`
	Queues        map[string]int `json:"queues"`
	OldestArrival *time.Time     `json:"oldestArrival,omitempty"`
}

type Listing struct {
	Summary  Summary   `json:"summary"`
	Matched  int       `json:"matched"`
	Messages []Message `json:"messages"`
}

type record struct {
	QueueName   string `json:"queue_name"`
	QueueID     string `json:"queue_id"`
	ArrivalTime int64  `json:"arrival_time"`
	MessageSize int64  `json:"message_size"`
	Sender      string `json:"sender"`
	Recipients  []struct {
		Address     string `json:"address"`
		DelayReason string `json:"delay_reason"`
	} `json:"recipients"`
}

func Decode(r io.Reader, each func(Message)) error {
	dec := json.NewDecoder(r)
	for {
		var rec record
		err := dec.Decode(&rec)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("parse postqueue output: %w", err)
		}
		m := Message{ID: rec.QueueID, Queue: rec.QueueName, ArrivalTime: time.Unix(rec.ArrivalTime, 0).UTC(), Size: rec.MessageSize, Sender: rec.Sender, Recipients: make([]Recipient, 0, len(rec.Recipients))}
		for _, rcpt := range rec.Recipients {
			m.Recipients = append(m.Recipients, Recipient{Address: rcpt.Address, DelayReason: rcpt.DelayReason})
		}
		each(m)
	}
}

func (s *Summary) add(m *Message) {
	s.Total++
	s.Queues[m.Queue]++
	if m.Queue != "hold" && (s.OldestArrival == nil || m.ArrivalTime.Before(*s.OldestArrival)) {
		at := m.ArrivalTime
		s.OldestArrival = &at
	}
}

func newSummary() Summary {
	s := Summary{Queues: map[string]int{}}
	for _, name := range Names {
		s.Queues[name] = 0
	}
	return s
}
