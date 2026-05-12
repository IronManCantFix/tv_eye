package service

import "testing"

func TestUpdateStatusSetsRecordingState(t *testing.T) {
	camID := "record-state-normal"
	deleteStatusForTest(t, camID)

	UpdateStatus(camID, true, "normal")

	StatusMux.RLock()
	status := StatusMap[camID]
	StatusMux.RUnlock()

	if status.RecordState != RecordStateRecording {
		t.Fatalf("expected record state %q, got %q", RecordStateRecording, status.RecordState)
	}
	if !status.IsRunning {
		t.Fatal("expected is_running to stay true")
	}
}

func TestUpdateRecordStateSetsMotionRecording(t *testing.T) {
	camID := "record-state-motion"
	deleteStatusForTest(t, camID)

	UpdateRecordState(camID, RecordStateMotionRecording, "normal")

	StatusMux.RLock()
	status := StatusMap[camID]
	StatusMux.RUnlock()

	if status.RecordState != RecordStateMotionRecording {
		t.Fatalf("expected record state %q, got %q", RecordStateMotionRecording, status.RecordState)
	}
	if !status.IsRunning {
		t.Fatal("expected motion recording to set is_running true")
	}
}

func TestUpdateRecordStateSetsMotionDetecting(t *testing.T) {
	camID := "record-state-motion-detecting"
	deleteStatusForTest(t, camID)

	UpdateRecordState(camID, RecordStateMotionDetecting, "normal")

	StatusMux.RLock()
	status := StatusMap[camID]
	StatusMux.RUnlock()

	if status.RecordState != RecordStateMotionDetecting {
		t.Fatalf("expected record state %q, got %q", RecordStateMotionDetecting, status.RecordState)
	}
	if !status.IsRunning {
		t.Fatal("expected motion detecting to set is_running true")
	}
}

func TestUpdateRecordStateNormalizesUnknownState(t *testing.T) {
	camID := "record-state-unknown"
	deleteStatusForTest(t, camID)

	UpdateRecordState(camID, "unknown", "normal")

	StatusMux.RLock()
	status := StatusMap[camID]
	StatusMux.RUnlock()

	if status.RecordState != RecordStateIdle {
		t.Fatalf("expected record state %q, got %q", RecordStateIdle, status.RecordState)
	}
	if status.IsRunning {
		t.Fatal("expected unknown record state to set is_running false")
	}
}

func deleteStatusForTest(t *testing.T, camID string) {
	t.Helper()

	StatusMux.Lock()
	oldStatus, hadStatus := StatusMap[camID]
	delete(StatusMap, camID)
	StatusMux.Unlock()

	t.Cleanup(func() {
		StatusMux.Lock()
		if hadStatus {
			StatusMap[camID] = oldStatus
		} else {
			delete(StatusMap, camID)
		}
		StatusMux.Unlock()
	})
}
