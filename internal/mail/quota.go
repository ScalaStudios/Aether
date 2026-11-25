package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

const usageBatch = 50

func (c *Client) Usage(ctx context.Context, users []string) (map[string]int64, error) {
	out := map[string]int64{}
	for start := 0; start < len(users); start += usageBatch {
		batch := users[start:min(start+usageBatch, len(users))]
		commands := make([]any, len(batch))
		for i, u := range batch {
			commands[i] = []any{"quotaGet", map[string]string{"user": u}, strconv.Itoa(i)}
		}
		_, resp, err := c.doveadmBatch(ctx, commands)
		if err != nil {
			return out, fmt.Errorf("doveadm quota get: %w", err)
		}
		for i, r := range resp {
			if used, ok := storageUsage(r); ok {
				out[batch[i]] = used
			}
		}
	}
	return out, nil
}

func storageUsage(r []json.RawMessage) (int64, bool) {
	var kind string
	var rows []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	if json.Unmarshal(r[0], &kind) != nil || kind != "doveadmResponse" || json.Unmarshal(r[1], &rows) != nil {
		return 0, false
	}
	for _, row := range rows {
		if row.Type == "STORAGE" {
			kib, err := strconv.ParseInt(row.Value, 10, 64)
			return kib * 1024, err == nil
		}
	}
	return 0, false
}
