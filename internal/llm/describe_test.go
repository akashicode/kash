package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDescriptions(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{
			name: "plain array",
			raw:  `[{"name":"Abhinavagupta","description":"10th-century philosopher who systematized Kashmir Shaivism."}]`,
			want: 1,
		},
		{
			name: "markdown fenced json",
			raw:  "```json\n[{\"name\":\"Gorakhnath\",\"description\":\"Legendary founder of the Nath tradition.\"}]\n```",
			want: 1,
		},
		{
			name: "surrounded by prose",
			raw:  "Here are the descriptions:\n[{\"name\":\"A\",\"description\":\"Desc A\"},{\"name\":\"B\",\"description\":\"Desc B\"}]\nEnd of output.",
			want: 2,
		},
		{
			name: "missing fields dropped",
			raw:  `[{"name":"","description":"Desc"},{"name":"Valid","description":""},{"name":"Full","description":"Has both"}]`,
			want: 1,
		},
		{
			name:    "no json array",
			raw:     "Just some plain text without any json",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDescriptions(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.want)
		})
	}
}

func TestDeterministicDescription(t *testing.T) {
	desc := DeterministicDescription("Abhinavagupta", []string{"authored Tantraloka", "disciple of Laksmanagupta"})
	assert.Contains(t, desc, "authored Tantraloka")
	assert.Contains(t, desc, "disciple of Laksmanagupta")

	empty := DeterministicDescription("Gorakhnath", nil)
	assert.Equal(t, "Gorakhnath", empty)
}

func TestDescribeEntitiesEmptyIsNoOp(t *testing.T) {
	var c *Client
	got, err := c.DescribeEntities(context.Background(), nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}
