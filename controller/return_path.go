package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/setting/system_setting"
)

func paymentReturnPath(suffix string) string {
	base := strings.TrimRight(system_setting.ServerAddress, "/")
	return base + suffix
}

func paymentReturnPathForHost(host string, suffix string) string {
	if isModelVisaHost(host) {
		return modelVisaServerAddress + suffix
	}
	return paymentReturnPath(suffix)
}

func paymentCallbackPathForHost(host, callbackAddress, suffix string) string {
	base := strings.TrimRight(callbackAddress, "/")
	if isModelVisaHost(host) {
		base = modelVisaServerAddress
	}
	return base + suffix
}
