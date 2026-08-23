package controlplane

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/owainlewis/factory/internal/protocol"
)

func (a *API) admitBuild(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.BuildRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	admission, err := a.store.AdmitBuild(r.Context(), input)
	if err != nil {
		var service *ServiceError
		if errors.As(err, &service) && service.Status < http.StatusInternalServerError {
			writeJSON(w, service.Status, protocol.ErrorBody{Error: protocol.APIError{
				Code: service.Code, Message: service.Message,
				AdmissionResult: protocol.AdmissionRejectedBeforeCommit,
				RequestKey:      input.RequestKey,
			}})
			return
		}
		writeError(w, err)
		return
	}
	if admission.Result == protocol.AdmissionAdmitted {
		a.logStateChange("run", admission.Run.Run.ID, string(admission.Run.Run.State))
		writeJSON(w, http.StatusCreated, admission)
		return
	}
	writeJSON(w, http.StatusOK, admission)
}

func (a *API) admitProcedureRun(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.ProcedureRunRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	admission, err := a.store.AdmitProcedureRun(r.Context(), input)
	if err != nil {
		var service *ServiceError
		if errors.As(err, &service) && service.Status < http.StatusInternalServerError {
			writeJSON(w, service.Status, protocol.ErrorBody{Error: protocol.APIError{
				Code: service.Code, Message: service.Message,
				AdmissionResult: protocol.AdmissionRejectedBeforeCommit,
				RequestKey:      input.RequestKey,
			}})
			return
		}
		writeError(w, err)
		return
	}
	if admission.Result == protocol.AdmissionAdmitted {
		a.logStateChange("run", admission.Run.Run.ID, string(admission.Run.Run.State))
		writeJSON(w, http.StatusCreated, admission)
		return
	}
	writeJSON(w, http.StatusOK, admission)
}

func (a *API) listProcedures(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r, defaultTaskPageSize, maxTaskPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := a.store.Tasks(r.Context(), true, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.ProcedurePage{
		Procedures: page.Tasks,
		NextCursor: page.NextCursor,
	})
}

func (a *API) listTasks(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r, defaultTaskPageSize, maxTaskPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	includeArchived := false
	if raw := r.URL.Query().Get("include_archived"); raw != "" {
		includeArchived, err = strconv.ParseBool(raw)
		if err != nil {
			writeError(w, invalid("invalid_query", "include_archived must be true or false"))
			return
		}
	}
	page, err := a.store.Tasks(r.Context(), includeArchived, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) createTask(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SaveTaskRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	task, err := a.store.CreateTask(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("task", task.ID, "created")
	writeJSON(w, http.StatusCreated, task)
}

func (a *API) getTask(w http.ResponseWriter, r *http.Request) {
	task, err := a.store.Task(r.Context(), r.PathValue("task_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (a *API) updateTask(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SaveTaskRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	task, err := a.store.UpdateTask(r.Context(), r.PathValue("task_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("task", task.ID, "updated")
	writeJSON(w, http.StatusOK, task)
}

func (a *API) setTaskArchived(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SetTaskArchivedRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	task, err := a.store.SetTaskArchived(r.Context(), r.PathValue("task_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("task", task.ID, "updated")
	writeJSON(w, http.StatusOK, task)
}

func (a *API) runTask(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.RunTaskRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	detail, created, err := a.store.RunTask(r.Context(), r.PathValue("task_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		a.logStateChange("run", detail.Run.ID, string(detail.Run.State))
	}
	writeJSON(w, status, detail)
}

func (a *API) discardTaskOccurrence(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.DiscardTaskOccurrenceRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	task, err := a.store.DiscardTaskOccurrence(r.Context(), r.PathValue("task_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("task", task.ID, "occurrence_discarded")
	writeJSON(w, http.StatusOK, task)
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r, defaultTaskPageSize, maxTaskPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	switch r.URL.Query().Get("view") {
	case "":
		page, err := a.store.RunPage(r.Context(), limit, r.URL.Query().Get("cursor"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	case "summary":
		page, err := a.store.RunSummaryPage(r.Context(), limit, r.URL.Query().Get("cursor"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	default:
		writeError(w, invalid("invalid_view", "view must be summary when provided"))
	}
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("view") {
	case "":
		detail, err := a.store.Run(r.Context(), r.PathValue("run_id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	case "summary":
		summary, err := a.store.RunSummary(r.Context(), r.PathValue("run_id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, summary)
	default:
		writeError(w, invalid("invalid_view", "view must be summary when provided"))
	}
}

func (a *API) cancelRun(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	detail, err := a.store.CancelRun(r.Context(), r.PathValue("run_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("run", detail.Run.ID, string(detail.Run.State))
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) retrySession(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	detail, err := a.store.RetrySession(r.Context(), r.PathValue("run_id"), r.PathValue("session_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("run", detail.Run.ID, string(detail.Run.State), "session_id", r.PathValue("session_id"))
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) cancelSession(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	detail, err := a.store.CancelSession(r.Context(), r.PathValue("run_id"), r.PathValue("session_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("run", detail.Run.ID, string(detail.Run.State), "session_id", r.PathValue("session_id"))
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) getOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := a.store.Overview(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}
