package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestPanelCompatEnvironmentAllowlist(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	t.Setenv("JWT_PANEL_COMPAT_USER_IDS", "145")
	var cfg Config
	require.NoError(t, viper.Unmarshal(&cfg))
	require.Equal(t, []int64{145}, cfg.JWT.PanelCompatUserIDs)
	t.Setenv("JWT_PANEL_COMPAT_USER_IDS", "")
	cfg = Config{} // Production config loads decode into a fresh value.
	require.NoError(t, viper.Unmarshal(&cfg))
	require.Empty(t, cfg.JWT.PanelCompatUserIDs)
}
