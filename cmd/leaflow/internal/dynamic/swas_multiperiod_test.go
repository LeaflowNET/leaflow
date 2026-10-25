package dynamic

import (
	"strings"
	"testing"

	"github.com/LeaflowNET/leaflow/pkg/spec"
	"github.com/spf13/cobra"
)

func TestMultiPeriodIncludePricesFlag(t *testing.T) {
	set, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	service, ok := set.Service("swas")
	if !ok {
		t.Fatal("swas missing")
	}
	op, ok := service.Operation("list-plans")
	if !ok {
		t.Fatal("list-plans missing")
	}
	for _, tt := range []struct {
		name  string
		flags []string
		want  string
		has   bool
	}{
		{"omitted", nil, "", false}, {"false", []string{"--include-prices=false"}, "false", true}, {"true", []string{"--include-prices"}, "true", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			binding := Bind(op)
			cmd := &cobra.Command{Use: "synthetic"}
			binding.Register(cmd)
			flag := cmd.Flags().Lookup("include-prices")
			if flag == nil {
				t.Fatal("native kebab flag missing")
			}
			if flag.DefValue != "false" {
				t.Fatalf("default: %s", flag.DefValue)
			}
			if err := cmd.ParseFlags(append([]string{"--region-id", "region-1"}, tt.flags...)); err != nil {
				t.Fatal(err)
			}
			_, query, _, err := binding.Values(cmd, nil)
			if err != nil {
				t.Fatal(err)
			}
			value, present := query["include_prices"]
			if present != tt.has || strings.Join(value, ",") != tt.want {
				t.Fatalf("query: %+v", query)
			}
			t.Logf("--include-prices present=%v value=%q; no transport", tt.has, tt.want)
		})
	}
}
