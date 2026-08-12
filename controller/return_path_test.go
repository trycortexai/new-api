package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
)

func TestPaymentReturnPathUsesDefaultDashboardRoutes(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://dashboard.example.com/"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	assert.Equal(
		t,
		"https://dashboard.example.com/wallet?pay=success",
		paymentReturnPath("/wallet?pay=success"),
	)
	assert.Equal(
		t,
		"https://dashboard.example.com/usage-logs",
		paymentReturnPath("/usage-logs"),
	)
}

func TestPaymentReturnPathForHostPreservesModelVisaOrigin(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://newapi.withcortex.ai/"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "exact host", host: "modelvisa.com", want: "https://modelvisa.com/wallet"},
		{name: "normalized host", host: "MODELvisa.com.:443", want: "https://modelvisa.com/wallet"},
		{name: "canonical host", host: "newapi.withcortex.ai", want: "https://newapi.withcortex.ai/wallet"},
		{name: "subdomain is not trusted", host: "pay.modelvisa.com", want: "https://newapi.withcortex.ai/wallet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, paymentReturnPathForHost(tt.host, "/wallet"))
		})
	}
}
