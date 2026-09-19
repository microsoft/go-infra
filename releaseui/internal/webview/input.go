package webview

// Input describes one browser control.
type Input struct {
	// ID is the JSON object key accepted by Process.Prepare.
	ID string `json:"id"`

	// Type selects the browser control. The release UI currently accepts "number".
	Type string `json:"type"`

	// Label names the control.
	Label string `json:"label"`

	// Description explains the value expected from the operator.
	Description string `json:"description,omitempty"`

	// Placeholder is example text shown by an empty number input.
	Placeholder string `json:"placeholder,omitempty"`
}
