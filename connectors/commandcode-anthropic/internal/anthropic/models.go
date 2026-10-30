package anthropic

type Model struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Created     int64  `json:"created,omitempty"`
	OwnedBy     string `json:"owned_by,omitempty"`
	Description string `json:"description,omitempty"`
}

type modelsListResponse struct {
	Data   []Model `json:"data"`
	Object string  `json:"object,omitempty"`
}
