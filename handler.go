package main

import (
	"encoding/json"
	"fmt"
	"github.com/Financial-Times/transactionid-utils-go"
	"github.com/gorilla/mux"
	"github.com/pborman/uuid"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
)

type Locker struct {
	locked chan bool
	acked  chan struct{}
}

func NewLocker() *Locker {
	lockedCh := make(chan bool)
	ackedCh := make(chan struct{})
	return &Locker{
		locked: lockedCh,
		acked:  ackedCh,
	}
}

type RequestHandler struct {
	FullExporter *FullExporter
	Inquirer     Inquirer
	*Locker
}

func NewRequestHandler(fullExporter *FullExporter, mongo DB, locker *Locker) *RequestHandler {
	return &RequestHandler{
		FullExporter: fullExporter,
		Inquirer:     &MongoInquirer{Mongo: mongo},
		Locker:       locker,
	}
}

func (handler *RequestHandler) Export(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()

	tid := transactionidutils.GetTransactionIDFromRequest(request)

	select {
	case handler.Locker.locked <- true:
		log.Info("Lock initiated")
	case <-time.After(time.Second * 3):
		msg := "Lock initiation timed out"
		log.Infof(msg)
		http.Error(writer, msg, http.StatusServiceUnavailable)
		return
	}

	select {
	case <-handler.Locker.acked:
		log.Info("Locker acquired")
	case <-time.After(time.Second * 20):
		msg := "Stopping kafka consumption timed out"
		log.Infof(msg)
		http.Error(writer, msg, http.StatusServiceUnavailable)
		return
	}

	candidates := getCandidateUuids(request)

	jobID := uuid.New()
	job := &Job{ID: jobID, NrWorker: handler.FullExporter.nrOfConcurrentWorkers, Status: STARTING}
	handler.FullExporter.AddJob(job)

	go func() {
		defer func() {
			log.Info("Locker released")
			handler.Locker.locked <- false
		}()
		log.Infoln("Calling mongo")
		docs, err, count := handler.Inquirer.Inquire("content", candidates)
		if err != nil {
			msg := fmt.Sprintf(`Failed to read IDs from mongo for %v! "%v"`, "content", err.Error())
			log.Info(msg)
			job.ErrorMessage = msg
			job.Status = FINISHED
			return
		}
		log.Infof("Nr of UUIDs found: %v", count)
		job.DocIds = docs
		job.Count = count

		job.RunFullExport(tid, handler.FullExporter.HandleContent)
	}()

	writer.WriteHeader(http.StatusAccepted)
	writer.Header().Add("Content-Type", "application/json")

	err := json.NewEncoder(writer).Encode(job)
	if err != nil {
		msg := fmt.Sprintf(`Failed to write job %v to response writer: "%v"`, job.ID, err)
		log.Warn(msg)
		fmt.Fprintf(writer, "{\"ID\": \"%v\"}", job.ID)
		return
	}
}

func getCandidateUuids(request *http.Request) (candidates []string) {
	var result map[string]interface{}
	body, err := ioutil.ReadAll(request.Body)
	if err != nil {
		log.Debugf("No valid POST body found, thus no candidate ids to export. Parsing error: %v", err)
		return
	}

	if err = json.Unmarshal(body, &result); err != nil {
		log.Debugf("No valid json body found, thus no candidate ids to export. Parsing error: %v", err)
		return
	}
	log.Infof("DEBUG Parsing request body: %v", result)
	ids, ok := result["ids"]
	if !ok {
		log.Debug("No ids field found in json body, thus no candidate ids to export.")
		return
	}
	idsString, ok := ids.(string)
	if ok {
		candidates = strings.Split(idsString, " ")
	}

	return
}

func (handler *RequestHandler) GetJob(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()

	vars := mux.Vars(request)
	jobID := vars["jobID"]

	writer.Header().Add("Content-Type", "application/json")

	job, err := handler.FullExporter.GetJob(jobID)
	if err != nil {
		msg := fmt.Sprintf(`{"message":"%v"}`, err)
		log.Info(msg)
		http.Error(writer, msg, http.StatusNotFound)
		return
	}

	err = json.NewEncoder(writer).Encode(job)
	if err != nil {
		msg := fmt.Sprintf(`Failed to write job %v to response writer: "%v"`, job.ID, err)
		log.Warn(msg)
		fmt.Fprintf(writer, "{\"ID\": \"%v\"}", job.ID)
		return
	}
}

func (handler *RequestHandler) GetRunningJobs(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()

	writer.Header().Add("Content-Type", "application/json")

	jobs := handler.FullExporter.GetRunningJobs()

	err := json.NewEncoder(writer).Encode(jobs)
	if err != nil {
		msg := fmt.Sprintf(`Failed to get running jobs: "%v"`, err)
		log.Warn(msg)
		fmt.Fprintf(writer, "{\"Jobs\": \"%v\"}", jobs)
		return
	}
}
