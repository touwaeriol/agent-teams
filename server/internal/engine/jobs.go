package engine

import (
	"context"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/jobs"
)

// Background jobs other than these run on their own (River works the timer jobs on every replica;
// the gateways, the SSH gateway and authorization reloads are per-replica plumbing).
const (
	fsSweepEvery     = time.Minute // provisioning, removals, deliverable moves
	orphanSweepEvery = time.Hour   // an ownerless volume goes at the second sweep that finds it
)

// registerJobs adds the engine's background jobs to e.Jobs (Start, before the registry starts).
func (e *Engine) registerJobs(ctx context.Context) {
	add := e.Jobs.Add
	add(jobs.Job{Name: "scheduler", Title: "任务派发", Lock: "agent-teams:scheduler", Every: e.Sched.Interval,
		Description: "把项目任务池里能领的任务派给空闲成员；有新任务时立即运行，此外按间隔整体检查一遍",
		Wake:        e.Sched.Signal(),
		Run: func(ctx context.Context, t jobs.Trigger) (string, error) {
			return e.Sched.Round(ctx, t == jobs.Woken), nil
		}})
	add(jobs.Job{Name: "merge-queue", Title: "合并队列", Lock: "agent-teams:merge", Every: e.Delivery.PollInterval,
		Description: "推进合并队列、PR 和 CI 状态；有合并提示时立即运行",
		Wake:        e.Delivery.Hints(ctx),
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { e.Delivery.Tick(ctx); return "", nil }})
	add(jobs.Job{Name: "timers", Title: "定时与恢复", Lock: "agent-teams:timers", Every: e.Timers.Interval,
		Description: "额度重置后恢复等待的讨论串、讨论串提醒、超时的会话迁移（定时任务本身由 River 执行）",
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { e.Timers.Tick(ctx); return "", nil }})
	add(jobs.Job{Name: "approvals", Title: "审批超时", Every: 15 * time.Second,
		Description: "按超时处理等太久的审批和组长审核（每个副本都运行）",
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { e.Approvals.Sweep(ctx); return "", nil }})
	add(jobs.Job{Name: "blobs", Title: "附件存储清理", Lock: "agent-teams:blobs", Every: time.Minute,
		Description: "删除待删的对象、补算哈希、中止过期的分片上传",
		Run: func(ctx context.Context, _ jobs.Trigger) (string, error) {
			st, err := (&blobgc.Worker{DB: e.Core.Pool, Blobs: e.Core.Blobs, Log: e.log}).RunOnce(ctx)
			return st.String(), err
		}})
	if !e.FS.Enabled() {
		return
	}
	add(jobs.Job{Name: "shared-folders", Title: "共享文件夹巡检", Lock: "agent-teams:shared-folders", Every: fsSweepEvery,
		Description: "给还没有共享文件夹的用户开通、重试没删掉的目录、把数据库里的交付物版本搬进卷",
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { return e.sweepSharedFolders(ctx) }})
	if e.memory != nil {
		add(jobs.Job{Name: "memory-files", Title: "记忆文件同步", Lock: "agent-teams:memory-files", Every: memorySweepEvery,
			Description: "把平台写的记忆写进文件夹、读回节点上的改动；每小时（或手动运行时）全量同步一次",
			Run:         e.memorySweep()})
	}
	add(jobs.Job{Name: "orphan-volumes", Title: "无主卷清理", Lock: "agent-teams:orphan-volumes", Every: orphanSweepEvery,
		Description: "销毁用户已删除但没销毁掉的共享文件夹（连续两次检查都没有用户才销毁）",
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { return e.sweepOrphanFolders(ctx) }})
}
