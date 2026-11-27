package userfs

import "errors"

// UnavailableMessage tells the user their shared folder cannot be used yet.
const UnavailableMessage = "共享文件夹正在自动开通或暂时不可用，稍后再试（状态见 设置 → 共享文件夹）"

// Unavailable reports whether err only means the shared folder is not configured on the console
// or the user has no ready volume (nothing to read or write, not a failure).
func Unavailable(err error) bool {
	return errors.Is(err, ErrDisabled) || IsNotFound(err)
}
