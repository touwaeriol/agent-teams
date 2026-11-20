/**
 * What an employee with remote servers is told (remote-servers.md §7.3): plain `ssh <name>`
 * through the platform, no user@, no tools with their own SSH implementation.
 */
export function sshPrompt(servers: string[]): string {
  return [
    "## Remote servers",
    `This session can reach these servers through the platform's SSH gateway: ${servers.join(", ")}.`,
    "Use the server name as the host and nothing else: `ssh <name> 'command'`, `scp file <name>:/path`, `sftp <name>`, `rsync -az dir/ <name>:/path`; git, ansible (inventory host = server name) and `docker -H ssh://<name>` work too.",
    "Do not write user@ (the username comes from the platform's settings), do not pass -i keys or edit ~/.ssh, and do not use tools with their own SSH implementation (paramiko, ssh2, Go/JS SSH libraries): they cannot reach the servers. The list can change during the session; `ssh <name>` explains a refusal on stderr.",
  ].join("\n");
}
