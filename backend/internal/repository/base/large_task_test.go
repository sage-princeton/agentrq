// Copyright 2026 Contextual, Inc. https://agentrq.com
// This notice may not be modified or removed.

package base

import (
	"context"
	"testing"
	"time"

	"github.com/agentrq/agentrq/backend/internal/data/model"
	"gorm.io/gorm"
)

// A task that had made more than 4,095 tool calls could no longer be written:
// every reply and every status change failed with "too many SQL variables".
// UpdateTask saved the task together with the tool calls GetTask had preloaded,
// as one batched statement of 8 bound values per row, and SQLite refuses a
// statement with more than 32,766 of them. The task was left ongoing, and it
// held the workspace's one ongoing slot.

// seedToolCalls writes n tool calls for dtTaskID, oldest first, with IDs and
// creation times that both increase the way monoflake IDs do in production.
func seedToolCalls(t *testing.T, db *gorm.DB, n int) time.Time {
	t.Helper()
	base := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	calls := make([]model.ToolCall, n)
	for i := range calls {
		calls[i] = model.ToolCall{
			ID:          dtTaskID + 1000 + int64(i),
			CreatedAt:   base.Add(time.Duration(i) * time.Second),
			TaskID:      dtTaskID,
			WorkspaceID: dtWorkspaceID,
			ToolName:    "Bash",
			Status:      "auto_allowed",
		}
	}
	if err := db.CreateInBatches(calls, 1000).Error; err != nil {
		t.Fatalf("seed tool calls: %v", err)
	}
	return base
}

func countToolCalls(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.ToolCall{}).Where("task_id = ?", dtTaskID).Count(&n).Error; err != nil {
		t.Fatalf("count tool calls: %v", err)
	}
	return n
}

func TestUpdateTask_TaskWithMoreToolCallsThanSQLiteCanBind(t *testing.T) {
	db := deleteTaskDB(t)
	seedTask(t, db)
	seedToolCalls(t, db, 4200)
	repo := New(&mockDB{db: db})
	ctx := context.Background()

	task, err := repo.GetTask(ctx, dtWorkspaceID, dtTaskID, dtUserID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	task.Status = "ongoing"
	if _, err := repo.UpdateTask(ctx, task); err != nil {
		t.Fatalf("UpdateTask on a task with 4,201 tool calls: %v", err)
	}

	var stored model.Task
	if err := db.First(&stored, dtTaskID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if stored.Status != "ongoing" {
		t.Errorf("status not saved: got %q", stored.Status)
	}
	if n := countToolCalls(t, db); n != 4201 {
		t.Errorf("tool calls changed by UpdateTask: got %d, want 4201", n)
	}
}

func TestUpdateTask_LeavesMessagesAndToolCallsAlone(t *testing.T) {
	db := deleteTaskDB(t)
	seedTask(t, db)
	repo := New(&mockDB{db: db})
	ctx := context.Background()

	task, err := repo.GetTask(ctx, dtWorkspaceID, dtTaskID, dtUserID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(task.Messages) != 1 || len(task.ToolCalls) != 1 {
		t.Fatalf("expected the seeded message and tool call to load, got %d and %d", len(task.Messages), len(task.ToolCalls))
	}
	task.Title = "renamed"
	task.Messages[0].Text = "edited in memory"
	task.ToolCalls[0].Status = "denied"
	task.Messages = append(task.Messages, model.Message{ID: 99, TaskID: dtTaskID, UserID: dtUserID, Sender: "agent", Text: "never created"})

	if _, err := repo.UpdateTask(ctx, task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	var stored model.Task
	if err := db.First(&stored, dtTaskID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if stored.Title != "renamed" {
		t.Errorf("task column not saved: got %q", stored.Title)
	}
	var msgs []model.Message
	db.Where("task_id = ?", dtTaskID).Find(&msgs)
	if len(msgs) != 1 || msgs[0].Text != "hello" {
		t.Errorf("messages written through UpdateTask: %+v", msgs)
	}
	var tc model.ToolCall
	db.First(&tc, 2)
	if tc.Status != "allowed" {
		t.Errorf("tool call written through UpdateTask: status %q", tc.Status)
	}
}

func TestGetTask_LoadsOnlyTheMostRecentToolCalls(t *testing.T) {
	db := deleteTaskDB(t)
	seedTask(t, db)
	base := seedToolCalls(t, db, TaskToolCallWindow+50)
	repo := New(&mockDB{db: db})

	task, err := repo.GetTask(context.Background(), dtWorkspaceID, dtTaskID, dtUserID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	assertMostRecentWindow(t, task.ToolCalls, base)
	if len(task.Messages) != 1 {
		t.Errorf("messages not loaded: got %d", len(task.Messages))
	}
}

func TestListToolCalls_ReturnsTheMostRecentWindow(t *testing.T) {
	db := deleteTaskDB(t)
	seedTask(t, db)
	base := seedToolCalls(t, db, TaskToolCallWindow+50)
	repo := New(&mockDB{db: db})

	calls, err := repo.ListToolCalls(context.Background(), dtTaskID)
	if err != nil {
		t.Fatalf("ListToolCalls: %v", err)
	}
	assertMostRecentWindow(t, calls, base)
}

// assertMostRecentWindow checks calls is the newest TaskToolCallWindow of the
// TaskToolCallWindow+50 seeded from base, oldest first.
func assertMostRecentWindow(t *testing.T, calls []model.ToolCall, base time.Time) {
	t.Helper()
	if len(calls) != TaskToolCallWindow {
		t.Fatalf("expected %d tool calls, got %d", TaskToolCallWindow, len(calls))
	}
	for i, tc := range calls {
		want := base.Add(time.Duration(50+i) * time.Second)
		if !tc.CreatedAt.Equal(want) {
			t.Fatalf("tool call %d: created %s, want %s (newest window, oldest first)", i, tc.CreatedAt, want)
		}
	}
}
