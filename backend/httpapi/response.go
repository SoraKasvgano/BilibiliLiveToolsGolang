package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
)

// Result is a compatibility response model aligned with the legacy C# API style.
type Result[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
	Data    T      `json:"data,omitempty"`
}

func OK[T any](w http.ResponseWriter, data T) {
	writeJSON(w, http.StatusOK, Result[T]{
		Code:    0,
		Message: "Success",
		Data:    data,
	})
}

func OKMessage(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusOK, Result[any]{
		Code:    0,
		Message: message,
	})
}

func Error(w http.ResponseWriter, code int, message string, status int) {
	if status <= 0 {
		status = http.StatusOK
	}
	writeJSON(w, status, Result[any]{
		Code:    code,
		Message: message,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("[httpapi] writeJSON encode error: %v", err)
	}
}
