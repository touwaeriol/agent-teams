package engine

import (
	"context"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/jobs"
	"github.com/touwaeriol/agent-teams/server/internal/msg"
	"github.com/touwaeriol/agent-teams/server/internal/timers"
)

// Every scheduled task is a built-in River job registered here (or in cmd/atctl: maintenance);
// what runs outside is per-replica plumbing (the gateways, the SSH gateway, authorization reloads,
// hint listeners).
const (
	fsSweepEvery     = time.Minute      // provisioning, removals, deliverable moves
	orphanSweepEvery = time.Hour        // an ownerless volume goes at the second sweep that finds it
	fsQuotaEvery     = 15 * time.Minute // shared folders over their capacity become read-only (stores without quotas)
)

// registerJobs adds the engine's background jobs to e.Jobs (Start, before the registry starts).
func (e *Engine) registerJobs(ctx context.Context) {
	add := e.Jobs.Add
	add(jobs.Job{Name: "scheduler", Title: msg.K("任务派发"), Every: e.Sched.Interval,
		Description: msg.K("把项目任务池里能领的任务派给空闲成员；有新任务时立即运行，此外按间隔整体检查一遍"),
		Wake:        e.Sched.Signal(),
		Run: func(ctx context.Context, t jobs.Trigger) (string, error) {
			return e.Sched.Round(ctx, t == jobs.Woken), nil
		}})
	add(jobs.Job{Name: "merge-queue", Title: msg.K("合并队列"), Every: e.Delivery.PollInterval,
		Description: msg.K("推进合并队列、PR 和 CI 状态；有合并提示时立即运行"),
		Wake:        e.Delivery.Hints(ctx),
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { e.Delivery.Tick(ctx); return "", nil }})
	add(jobs.Job{Name: "timers", Title: msg.K("定时与恢复"), Every: e.Timers.Interval,
		Description: msg.K("额度重置后恢复等待的讨论串、讨论串提醒、超时的会话迁移"),
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { e.Timers.Tick(ctx); return "", nil }})
	add(jobs.Job{Name: "schedules", Title: msg.K("用户定时任务对账"), Every: timers.ReconcileEvery,
		Description: msg.K("给缺了下一次运行的用户定时任务补上，删除结束很久的定时任务（定时任务到点由 River 逐个触发）"),
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { return "", e.Timers.Reconcile(ctx) }})
	add(jobs.Job{Name: "approvals", Title: msg.K("审批超时"), Every: 15 * time.Second,
		Description: msg.K("按超时处理等太久的审批和组长审核"),
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { e.Approvals.Sweep(ctx); return "", nil }})
	add(jobs.Job{Name: "blobs", Title: msg.K("附件存储清理"), Every: time.Minute,
		Description: msg.K("删除待删的对象、补算哈希、中止过期的分片上传"),
		Run: func(ctx context.Context, _ jobs.Trigger) (string, error) {
			st, err := (&blobgc.Worker{DB: e.Core.Pool, Blobs: e.Core.Blobs, Log: e.log}).RunOnce(ctx)
			return st.String(), err
		}})
	if !e.FS.Enabled() {
		return
	}
	add(jobs.Job{Name: "shared-folders", Title: msg.K("共享文件夹巡检"), Every: fsSweepEvery,
		Description: msg.K("给还没有共享文件夹的用户开通、重试没删掉的目录、把数据库里的交付物版本搬进卷"),
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { return e.sweepSharedFolders(ctx) }})
	if e.memory != nil {
		add(jobs.Job{Name: "memory-files", Title: msg.K("记忆文件同步"), Every: memorySweepEvery,
			Description: msg.K("把平台写的记忆写进文件夹、读回节点上的改动；每小时（或手动运行时）全量同步一次"),
			Run:         e.memorySweep()})
	}
	add(jobs.Job{Name: "orphan-volumes", Title: msg.K("无主共享文件夹清理"), Every: orphanSweepEvery,
		Description: msg.K("删除用户已删除但没删掉的共享文件夹和对象存储密钥（连续两次检查都没有用户才删）"),
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { return e.sweepOrphanFolders(ctx) }})
	if !e.FS.SoftQuota() {
		return // the store enforces the capacity itself (RustFS bucket quotas)
	}
	add(jobs.Job{Name: "shared-folder-quota", Title: msg.K("共享文件夹容量"), Every: fsQuotaEvery,
		Description: msg.K("统计每个共享文件夹的用量：超出容量的换成不能写的密钥（还能读和删），降回容量以内再恢复"),
		Run:         func(ctx context.Context, _ jobs.Trigger) (string, error) { return e.enforceFolderQuotas(ctx) }})
}
