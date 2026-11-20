package brain

// SharedItem is one addressed item in the shared layer (cmd_shared.go): a note, a prompt,
// a proposed task or a fact. Read by the addressee only, and carrying no authority.
type SharedItem struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	From       string `json:"from"`
	To         string `json:"to"`
	AuthoredBy string `json:"authored_by"`
	Title      string `json:"title,omitempty"`
	Body       string `json:"body"`
	Payload    string `json:"payload,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	ReadAt     *int64 `json:"read_at,omitempty"`
	AcceptedAs *int64 `json:"accepted_as,omitempty"`
}
