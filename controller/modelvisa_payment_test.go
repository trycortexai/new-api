package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentMethodAvailabilityForModelVisa(t *testing.T) {
	enableModelVisaHostPolicy(t)

	tests := []struct {
		name   string
		host   string
		method string
		want   bool
	}{
		{name: "Creem hidden", host: modelVisaHost, method: model.PaymentMethodCreem, want: false},
		{name: "Pancake hidden", host: modelVisaHost, method: model.PaymentMethodWaffoPancake, want: false},
		{name: "Stripe remains", host: modelVisaHost, method: model.PaymentMethodStripe, want: true},
		{name: "Creem canonical remains", host: "newapi.withcortex.ai", method: model.PaymentMethodCreem, want: true},
		{name: "untrusted subdomain remains canonical", host: "pay.modelvisa.com", method: model.PaymentMethodWaffoPancake, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isPaymentMethodAvailableForHost(tt.host, tt.method))
		})
	}
}

func TestUnsupportedModelVisaPaymentHandlersRejectBeforeCheckout(t *testing.T) {
	enableModelVisaHostPolicy(t)
	gin.SetMode(gin.TestMode)

	handlers := []struct {
		name    string
		path    string
		body    string
		handler gin.HandlerFunc
	}{
		{name: "Creem top-up", path: "/api/user/creem", body: `{}`, handler: RequestCreemPay},
		{name: "Creem subscription", path: "/api/subscription/creem", body: `{}`, handler: SubscriptionRequestCreemPay},
		{name: "Pancake top-up", path: "/api/user/waffo-pancake", body: `{}`, handler: RequestWaffoPancakePay},
		{name: "Pancake subscription", path: "/api/subscription/waffo-pancake", body: `{}`, handler: SubscriptionRequestWaffoPancakePay},
	}

	for _, tt := range handlers {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			context.Request.Host = modelVisaHost
			context.Request.Header.Set("Content-Type", "application/json")

			tt.handler(context)

			var payload map[string]any
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			assert.Equal(t, false, payload["success"])
			assert.Contains(t, payload["message"], "ModelVisa 暂不支持")
		})
	}
}
