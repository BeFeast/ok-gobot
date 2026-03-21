package control

import "strings"

// handleControlQuery dispatches control-plane query commands.
// Returns true if the command was handled.
func (s *Server) handleControlQuery(c *client, cmd ClientMsg) bool {
	switch cmd.Type {
	case CmdListJobs:
		s.handleListJobs(c, cmd)
	case CmdGetJob:
		s.handleGetJob(c, cmd)
	case CmdCancelJob:
		s.handleCancelJob(c, cmd)
	case CmdListWorkers:
		s.handleListWorkers(c)
	case CmdListRoutes:
		s.handleListRoutes(c, cmd)
	default:
		return false
	}
	return true
}

func (s *Server) handleListJobs(c *client, cmd ClientMsg) {
	if s.store == nil {
		c.sendTUIError("job storage not available")
		return
	}
	limit := cmd.Limit
	if limit <= 0 {
		limit = 50
	}
	status := strings.TrimSpace(cmd.Status)
	jobs, err := s.store.ListJobsByStatus(status, limit)
	if err != nil {
		c.sendTUIError("list jobs: " + err.Error())
		return
	}
	c.sendTUIMsg(ServerMsg{
		Type: MsgTypeJobs,
		Data: map[string]interface{}{
			"jobs":  jobs,
			"count": len(jobs),
		},
	})
}

func (s *Server) handleGetJob(c *client, cmd ClientMsg) {
	if s.store == nil {
		c.sendTUIError("job storage not available")
		return
	}
	jobID := strings.TrimSpace(cmd.JobID)
	if jobID == "" {
		c.sendTUIError("job_id is required")
		return
	}
	job, err := s.store.GetJob(jobID)
	if err != nil {
		c.sendTUIError("get job: " + err.Error())
		return
	}
	if job == nil {
		c.sendTUIError("job not found")
		return
	}
	events, err := s.store.ListJobEvents(jobID, 100)
	if err != nil {
		c.sendTUIError("list events: " + err.Error())
		return
	}
	artifacts, err := s.store.ListJobArtifacts(jobID, 100)
	if err != nil {
		c.sendTUIError("list artifacts: " + err.Error())
		return
	}
	c.sendTUIMsg(ServerMsg{
		Type: MsgTypeJob,
		Data: map[string]interface{}{
			"job":       job,
			"events":    events,
			"artifacts": artifacts,
		},
	})
}

func (s *Server) handleCancelJob(c *client, cmd ClientMsg) {
	if s.jobService == nil {
		c.sendTUIError("job service not available")
		return
	}
	jobID := strings.TrimSpace(cmd.JobID)
	if jobID == "" {
		c.sendTUIError("job_id is required")
		return
	}
	if err := s.jobService.Cancel(jobID); err != nil {
		c.sendTUIError("cancel job: " + err.Error())
		return
	}
	c.sendTUIMsg(ServerMsg{
		Type:    MsgTypeJob,
		Message: "cancel requested for job " + jobID,
		Data: map[string]interface{}{
			"job_id":    jobID,
			"cancelled": true,
		},
	})
}

func (s *Server) handleListWorkers(c *client) {
	if s.workerHub == nil {
		c.sendTUIError("runtime hub not available")
		return
	}
	workers := s.workerHub.WorkerSnapshots()
	c.sendTUIMsg(ServerMsg{
		Type: MsgTypeWorkers,
		Data: map[string]interface{}{
			"workers": workers,
			"count":   len(workers),
		},
	})
}

func (s *Server) handleListRoutes(c *client, cmd ClientMsg) {
	if s.routeLog == nil {
		c.sendTUIError("route log not available")
		return
	}
	limit := cmd.Limit
	if limit <= 0 {
		limit = 50
	}
	records := s.routeLog.Recent(limit)
	c.sendTUIMsg(ServerMsg{
		Type: MsgTypeRoutes,
		Data: map[string]interface{}{
			"routes": records,
			"count":  len(records),
		},
	})
}
