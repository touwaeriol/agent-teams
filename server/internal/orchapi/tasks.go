package orchapi

import (
	"context"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

type tasksInput struct {
	TID    string `path:"tid" maxLength:"64"`
	Status string `query:"status" doc:"Comma separated statuses"`
	Main   string `query:"main" doc:"Plan step id"`
}

type tasksOutput struct{ Body []core.TaskView }

type taskInput struct {
	TID  string `path:"tid" maxLength:"64"`
	Task string `path:"task" maxLength:"16"`
}

type taskOutput struct{ Body core.TaskView }

func (a *API) registerTasks(api huma.API) {
	huma.Register(api, op("list-tasks", "GET", "/api/threads/{tid}/tasks", "tasks", summary("Task pool board")), a.listTasks)
	huma.Register(api, op("cancel-task", "POST", "/api/threads/{tid}/tasks/{task}/cancel", "tasks"), a.cancelTask)
	huma.Register(api, op("retry-task", "POST", "/api/threads/{tid}/tasks/{task}/retry", "tasks",
		summary("Put a failed, blocked or cancelled task back into the pool")), a.retryTask)
}

func (a *API) listTasks(ctx context.Context, in *tasksInput) (*tasksOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	f := core.TaskFilter{MainID: in.Main}
	if in.Status != "" {
		f.Status = strings.Split(in.Status, ",")
	}
	tasks, err := core.ThreadTasks(ctx, a.e.Core.Pool, t.ID, f)
	out := make([]core.TaskView, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, core.TaskViewOf(task, t.No))
	}
	return &tasksOutput{Body: out}, err
}

func (a *API) cancelTask(ctx context.Context, in *taskInput) (*taskOutput, error) {
	return a.taskAction(ctx, in, func(ctx context.Context, tx *core.Tx, task core.Task) (core.Task, error) {
		return a.e.Sched.Cancel(ctx, tx, task, "用户取消了这个任务。")
	})
}

func (a *API) retryTask(ctx context.Context, in *taskInput) (*taskOutput, error) {
	return a.taskAction(ctx, in, a.e.Sched.Retry)
}

func (a *API) taskAction(ctx context.Context, in *taskInput, fn func(context.Context, *core.Tx, core.Task) (core.Task, error)) (*taskOutput, error) {
	_, t, err := a.thread(ctx, in.TID)
	if err != nil {
		return nil, err
	}
	var out core.Task
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		task, err := core.LockTask(ctx, tx, t.ID, in.Task)
		if err != nil {
			return err
		}
		out, err = fn(ctx, tx, task)
		return err
	})
	if err != nil {
		return nil, fail(err, "没有这个任务")
	}
	return &taskOutput{Body: core.TaskViewOf(out, t.No)}, nil
}
