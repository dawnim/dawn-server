package supervisor

import "testing"

func TestProcessName(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"dawn-simple", []string{"dawn-simple", "horizon:supervisor", "multiples"}, "multiples"},
		{"dawn-simple", []string{"dawn-simple", "horizon:work", "redis", "--name=supervisor-1"}, "supervisor-1"},
		{"dawn-simple", []string{"dawn-simple", "dawn:service", "http"}, "dawn-simple"},
	} {
		if got := processName(test.name, test.args); got != test.want {
			t.Errorf("processName(%q, %v) = %q, want %q", test.name, test.args, got, test.want)
		}
	}
}
