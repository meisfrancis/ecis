package awsx

import (
	"fmt"
	"os/exec"
)

// ExecRequest describes an interactive session into a running container.
type ExecRequest struct {
	Cluster   string
	Task      string
	Container string
	Command   string
	Profile   string
	Region    string
}

// ExecCommand builds the argv for an ECS Exec session.
//
// ecis shells out to the AWS CLI rather than driving the SSM protocol itself,
// for the same reason k9s shells out to kubectl: an interactive session needs a
// real PTY and the session-manager-plugin already handles the websocket
// multiplexing correctly.
func (r ExecRequest) ExecCommand() (string, []string, error) {
	bin, err := exec.LookPath("aws")
	if err != nil {
		return "", nil, fmt.Errorf("the AWS CLI is required for exec: %w", err)
	}
	if _, err := exec.LookPath("session-manager-plugin"); err != nil {
		return "", nil, fmt.Errorf("session-manager-plugin is required for exec: %w", err)
	}

	cmd := r.Command
	if cmd == "" {
		cmd = "/bin/sh"
	}
	args := []string{
		"ecs", "execute-command",
		"--cluster", r.Cluster,
		"--task", r.Task,
		"--interactive",
		"--command", cmd,
	}
	if r.Container != "" {
		args = append(args, "--container", r.Container)
	}
	if r.Profile != "" {
		args = append(args, "--profile", r.Profile)
	}
	if r.Region != "" {
		args = append(args, "--region", r.Region)
	}
	return bin, args, nil
}
