package mail

import (
	"encoding/json"
	"testing"
)

func TestStorageUsage(t *testing.T) {
	var out [][]json.RawMessage
	resp := `[["doveadmResponse",[{"root":"User quota","type":"STORAGE","value":"401","limit":"1024","percent":"39"},{"root":"User quota","type":"MESSAGE","value":"2","limit":"-","percent":"0"}],"0"],["error",{"type":"exitCode", "exitCode":67},"1"]]`
	if err := json.Unmarshal([]byte(resp), &out); err != nil {
		t.Fatal(err)
	}
	if used, ok := storageUsage(out[0]); !ok || used != 401*1024 {
		t.Fatalf("storageUsage = %d, %v; want %d, true", used, ok, 401*1024)
	}
	if _, ok := storageUsage(out[1]); ok {
		t.Fatal("an unknown user should not report usage")
	}
}
