package controlplane

import (
	"net/http"

	"github.com/owainlewis/factory/internal/protocol"
)

func (a *API) listPipelines(w http.ResponseWriter, r *http.Request) {
	page, err := a.store.Pipelines(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) getPipeline(w http.ResponseWriter, r *http.Request) {
	pipeline, err := a.store.Pipeline(r.Context(), r.PathValue("pipeline_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pipeline)
}

func (a *API) createPipeline(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SavePipelineRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	pipeline, err := a.store.CreatePipeline(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("pipeline", pipeline.ID, "created")
	writeJSON(w, http.StatusCreated, pipeline)
}

func (a *API) updatePipeline(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SavePipelineRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	pipeline, err := a.store.UpdatePipeline(r.Context(), r.PathValue("pipeline_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("pipeline", pipeline.ID, "updated")
	writeJSON(w, http.StatusOK, pipeline)
}

func (a *API) deletePipeline(w http.ResponseWriter, r *http.Request) {
	if !validateMutationOrigin(w, r) {
		return
	}
	if err := a.store.DeletePipeline(r.Context(), r.PathValue("pipeline_id")); err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("pipeline", r.PathValue("pipeline_id"), "deleted")
	w.WriteHeader(http.StatusNoContent)
}
