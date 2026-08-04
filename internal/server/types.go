package server

type Response struct {
	Code    int         `json:"code"`
	Data    interface{} `json:"data"`
	Message string      `json:"message"`
}

type Job struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status string `json:"status"`
}