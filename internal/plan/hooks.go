package plan

// Hooks are the shell commands a node runs around its execution.
//
// The type lives here rather than in internal/hook so a plan can declare
// hooks without the plan package depending on the hook package.
type Hooks struct {
	Pre    string `yaml:"pre,omitempty" json:"pre,omitempty"`
	Post   string `yaml:"post,omitempty" json:"post,omitempty"`
	OnFail string `yaml:"on_fail,omitempty" json:"on_fail,omitempty"`
}
