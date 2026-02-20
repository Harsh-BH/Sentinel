package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	mockpub "github.com/Harsh-BH/Sentinel/api/internal/publisher/mock"
	mockrepo "github.com/Harsh-BH/Sentinel/api/internal/repository/mock"
	"github.com/Harsh-BH/Sentinel/api/internal/usecase"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func setupTestRouter() (*gin.Engine, *mockrepo.MockJobRepository, *mockpub.MockPublisher) {
	repo := mockrepo.NewMockJobRepository()
	pub := mockpub.NewMockPublisher()
	logger := zap.NewNop()

	submitUC := usecase.NewSubmitJobUsecase(repo, pub, logger)
	getJobUC := usecase.NewGetJobUsecase(repo, logger)

	router := gin.New()
	subHandler := NewSubmissionHandler(submitUC, getJobUC, logger)

	router.POST("/api/v1/submissions", subHandler.Submit)
	router.GET("/api/v1/submissions/:id", subHandler.GetByID)

	return router, repo, pub
}

func TestSubmitHandler_Success(t *testing.T) {
	router, _, pub := setupTestRouter()

	body := map[string]interface{}{
		"language":    "python",
		"source_code": "print('hello')",
		"stdin":       "test",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}

	var resp domain.SubmitResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.JobID.String() == "" {
		t.Error("expected non-empty job ID")
	}
	if len(pub.Published) != 1 {
		t.Errorf("expected 1 published job, got %d", len(pub.Published))
	}
}

func TestSubmitHandler_InvalidLanguage(t *testing.T) {
	router, _, _ := setupTestRouter()

	body := map[string]interface{}{
		"language":    "ruby",
		"source_code": "puts 'hello'",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitHandler_EmptyBody(t *testing.T) {
	router, _, _ := setupTestRouter()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", bytes.NewBuffer([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitHandler_MissingSourceCode(t *testing.T) {
	router, _, _ := setupTestRouter()

	body := map[string]interface{}{
		"language": "python",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetByIDHandler_Success(t *testing.T) {
	router, _, _ := setupTestRouter()

	// First submit a job
	body := map[string]interface{}{
		"language":    "python",
		"source_code": "print('hello')",
	}
	jsonBody, _ := json.Marshal(body)

	submitReq := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", bytes.NewBuffer(jsonBody))
	submitReq.Header.Set("Content-Type", "application/json")
	submitW := httptest.NewRecorder()
	router.ServeHTTP(submitW, submitReq)

	var submitResp domain.SubmitResponse
	json.Unmarshal(submitW.Body.Bytes(), &submitResp)

	// Now get the job
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/submissions/"+submitResp.JobID.String(), nil)
	getW := httptest.NewRecorder()
	router.ServeHTTP(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", getW.Code, getW.Body.String())
	}

	var job domain.Job
	if err := json.Unmarshal(getW.Body.Bytes(), &job); err != nil {
		t.Fatalf("failed to unmarshal job: %v", err)
	}
	if job.JobID != submitResp.JobID {
		t.Errorf("expected job ID %s, got %s", submitResp.JobID, job.JobID)
	}
	if job.Status != domain.StatusQueued {
		t.Errorf("expected status QUEUED, got %s", job.Status)
	}
}

func TestGetByIDHandler_NotFound(t *testing.T) {
	router, _, _ := setupTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/submissions/00000000-0000-0000-0000-000000000001", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetByIDHandler_InvalidUUID(t *testing.T) {
	router, _, _ := setupTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/submissions/not-a-uuid", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSubmitHandler_CppLanguage(t *testing.T) {
	router, repo, _ := setupTestRouter()

	body := map[string]interface{}{
		"language":    "cpp",
		"source_code": "#include <iostream>\nint main() { std::cout << \"hello\"; }",
		"stdin":       "",
	}
	jsonBody, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}

	jobs := repo.GetAll()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Language != domain.LangCpp {
		t.Errorf("expected language cpp, got %s", jobs[0].Language)
	}
}

func TestLanguageHandler(t *testing.T) {
	handler := NewLanguageHandler()

	router := gin.New()
	router.GET("/api/v1/languages", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/languages", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp map[string][]domain.LanguageInfo
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	languages := resp["languages"]
	if len(languages) != 2 {
		t.Errorf("expected 2 languages, got %d", len(languages))
	}
}
