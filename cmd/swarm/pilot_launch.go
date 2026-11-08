package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type ownerLaunchClient interface {
	LaunchOperation(string, protocol.LaunchReq) (string, string, error)
}

type pilotLaunchPending struct {
	OperationID string             `json:"operation_id"`
	Kind        string             `json:"kind"`
	Arguments   []string           `json:"arguments"`
	Request     protocol.LaunchReq `json:"request"`
	Settled     bool               `json:"settled"`
}

// The pilot lock covers preparation through receipt output. An uncertain create
// and resume share one guard so neither can replace the other's unknown worker.
func pilotLaunch(path, kind string, args []string, c agentClient, build func() (protocol.LaunchReq, error)) (pilotLaunchPending, string, string, error) {
	var pending pilotLaunchPending
	launcher, ok := c.(ownerLaunchClient)
	if !ok {
		return pending, "", "", errors.New("reliable launch requires an updated daemon")
	}
	file := filepath.Join(path, "launch-pending")
	body, err := os.ReadFile(file)
	if err == nil {
		if json.Unmarshal(body, &pending) != nil || pending.OperationID == "" || pending.Kind == "" {
			return pending, "", "", errors.New("saved launch is corrupt; reconcile before launching new work")
		}
		if !pending.Settled && (pending.Kind != kind || !slices.Equal(pending.Arguments, args)) {
			return pending, "", "", fmt.Errorf("operation_id=%s: another launch has an unresolved outcome; retry its exact command", pending.OperationID)
		}
	} else if !os.IsNotExist(err) {
		return pending, "", "", err
	}
	if pending.OperationID == "" || pending.Settled {
		req, err := build()
		if err != nil {
			return pending, "", "", err
		}
		req.Env = daemon.OwnerLaunchEnvironment(req.Agent, req.Env)
		id, err := protocol.NewLaunchOperationID()
		if err != nil {
			return pending, "", "", err
		}
		pending = pilotLaunchPending{OperationID: id, Kind: kind, Arguments: args, Request: req}
		if err := savePilotRecord(file, pending); err != nil {
			return pending, "", "", err
		}
	}
	id, name, err := launcher.LaunchOperation(pending.OperationID, pending.Request)
	if err != nil {
		err = fmt.Errorf("operation_id=%s: %w", pending.OperationID, err)
	}
	return pending, id, name, err
}

func settlePilotLaunch(path, operationID string) error {
	var pending pilotLaunchPending
	body, err := os.ReadFile(filepath.Join(path, "launch-pending"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := json.Unmarshal(body, &pending); err != nil {
		return err
	}
	if pending.OperationID != operationID {
		return nil
	}
	pending.Settled = true
	return savePilotRecord(filepath.Join(path, "launch-pending"), pending)
}

func pilotLaunchStatus(args []string, c agentClient, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return pilotMisuse(stderr, "launch-status needs one operation ID")
	}
	reader, ok := c.(interface {
		LaunchOperationStatus(string) (schema.OperationOutcomeView, error)
	})
	if !ok {
		_, _ = fmt.Fprintln(stderr, "pilot: launch status requires an updated daemon")
		return 1
	}
	out, err := reader.LaunchOperationStatus(args[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pilot: launch status: %v\n", err)
		return 1
	}
	return pilotJSON(stdout, stderr, map[string]any{"operation_id": args[0], "operation": out})
}
