package bot

import (
	"testing"
)

func TestIsDangerous(t *testing.T) {
	am := &ApprovalManager{}

	tests := []struct {
		name     string
		command  string
		expected bool
	}{
		{
			name:     "rm -rf is dangerous",
			command:  "rm -rf /tmp/test",
			expected: true,
		},
		{
			name:     "rm -r is dangerous",
			command:  "rm -r /home/user/folder",
			expected: true,
		},
		{
			name:     "kill command is dangerous",
			command:  "kill -9 1234",
			expected: true,
		},
		{
			name:     "killall is dangerous",
			command:  "killall nginx",
			expected: true,
		},
		{
			name:     "shutdown is dangerous",
			command:  "shutdown -h now",
			expected: true,
		},
		{
			name:     "reboot is dangerous",
			command:  "reboot",
			expected: true,
		},
		{
			name:     "dd is dangerous",
			command:  "dd if=/dev/zero of=/dev/sda",
			expected: true,
		},
		{
			name:     "mkfs is dangerous",
			command:  "mkfs.ext4 /dev/sdb1",
			expected: true,
		},
		{
			name:     "fdisk is dangerous",
			command:  "fdisk /dev/sda",
			expected: true,
		},
		{
			name:     "format is dangerous",
			command:  "format c:",
			expected: true,
		},
		{
			name:     "passwd is dangerous",
			command:  "passwd root",
			expected: true,
		},
		{
			name:     "chmod 777 is dangerous",
			command:  "chmod 777 /etc/passwd",
			expected: true,
		},
		{
			name:     "chown is dangerous",
			command:  "chown nobody:nobody /etc/shadow",
			expected: true,
		},
		{
			name:     "iptables is dangerous",
			command:  "iptables -F",
			expected: true,
		},
		{
			name:     "systemctl stop is dangerous",
			command:  "systemctl stop sshd",
			expected: true,
		},
		{
			name:     "systemctl disable is dangerous",
			command:  "systemctl disable firewalld",
			expected: true,
		},
		{
			name:     "docker rm is dangerous",
			command:  "docker rm -f $(docker ps -aq)",
			expected: true,
		},
		{
			name:     "DROP TABLE is dangerous",
			command:  "DROP TABLE users;",
			expected: true,
		},
		{
			name:     "DELETE FROM is dangerous",
			command:  "DELETE FROM users WHERE 1=1;",
			expected: true,
		},
		{
			name:     "truncate is dangerous",
			command:  "truncate -s 0 /var/log/syslog",
			expected: true,
		},
		{
			name:     "pkill is dangerous",
			command:  "pkill -9 postgres",
			expected: true,
		},
		{
			name:     "halt is dangerous",
			command:  "halt",
			expected: true,
		},
		{
			name:     "poweroff is dangerous",
			command:  "poweroff",
			expected: true,
		},
		{
			name:     "safe ls command",
			command:  "ls -la /home",
			expected: false,
		},
		{
			name:     "safe cat command",
			command:  "cat /etc/hosts",
			expected: false,
		},
		{
			name:     "safe grep command",
			command:  "grep -r 'pattern' /var/log",
			expected: false,
		},
		{
			name:     "safe mkdir command",
			command:  "mkdir -p /tmp/test",
			expected: false,
		},
		{
			name:     "safe cp command",
			command:  "cp file.txt backup.txt",
			expected: false,
		},
		{
			name:     "safe mv command",
			command:  "mv oldname.txt newname.txt",
			expected: false,
		},
		{
			name:     "safe echo command",
			command:  "echo 'Hello World'",
			expected: false,
		},
		{
			name:     "safe systemctl status",
			command:  "systemctl status nginx",
			expected: false,
		},
		{
			name:     "safe docker ps",
			command:  "docker ps -a",
			expected: false,
		},
		{
			name:     "safe rm single file",
			command:  "rm file.txt",
			expected: false,
		},
		{
			name:     "case insensitive - RM -RF",
			command:  "RM -RF /tmp/test",
			expected: true,
		},
		{
			name:     "case insensitive - SHUTDOWN",
			command:  "SHUTDOWN NOW",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := am.IsDangerous(tt.command)
			if result != tt.expected {
				t.Errorf("IsDangerous(%q) = %v, want %v", tt.command, result, tt.expected)
			}
		})
	}
}
