package legacy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetQrisMerchantBuildsSOAPRequestAndParsesResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("SOAPAction"); got != "getQrisMerchant" {
			t.Fatalf("unexpected SOAPAction: %s", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}

		requestXML := string(body)
		if !strings.Contains(requestXML, `<ns:getQrisMerchant>`) {
			t.Fatalf("expected getQrisMerchant operation, got %s", requestXML)
		}
		if !strings.Contains(requestXML, `<ns:args0>MERCHANT001</ns:args0>`) {
			t.Fatalf("expected merchant code arg, got %s", requestXML)
		}

		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">
  <soapenv:Body>
    <ns:getQrisMerchantResponse xmlns:ns="http://BankService.services.axis2">
      <ns:return>OK|MERCHANT001|Legacy Shop</ns:return>
    </ns:getQrisMerchantResponse>
  </soapenv:Body>
</soapenv:Envelope>`))
	}))
	defer server.Close()

	client := &Client{
		endpoint:   server.URL,
		httpClient: server.Client(),
	}

	merchant, err := client.GetQrisMerchant(context.Background(), "MERCHANT001")
	if err != nil {
		t.Fatalf("GetQrisMerchant returned error: %v", err)
	}

	if merchant.Code != "MERCHANT001" {
		t.Fatalf("unexpected merchant code: %s", merchant.Code)
	}
	if merchant.Name != "Legacy Shop" {
		t.Fatalf("unexpected merchant name: %s", merchant.Name)
	}
}

func TestLoginParsesSessionAndExpiry(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">
  <soapenv:Body>
    <ns:loginResponse xmlns:ns="http://BankService.services.axis2">
      <ns:return>OK|CUST123|2623860486223779|John Doe|SESS-ABC|1718097600</ns:return>
    </ns:loginResponse>
  </soapenv:Body>
</soapenv:Envelope>`))
	}))
	defer server.Close()

	client := &Client{
		endpoint:   server.URL,
		httpClient: server.Client(),
	}

	result, err := client.Login(context.Background(), "john@example.com", "secret")
	if err != nil {
		t.Fatalf("Login returned error: %v", err)
	}

	if result.CustomerID != "CUST123" {
		t.Fatalf("unexpected customer id: %s", result.CustomerID)
	}
	if result.SessionReference != "SESS-ABC" {
		t.Fatalf("unexpected session reference: %s", result.SessionReference)
	}

	expectedExpiry := time.Unix(1718097600, 0).UTC()
	if !result.ExpiresAt.Equal(expectedExpiry) {
		t.Fatalf("unexpected expiry: got %s want %s", result.ExpiresAt, expectedExpiry)
	}
}

func TestPingUsesWsdl(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.URL.RawQuery != "wsdl" {
			t.Fatalf("expected wsdl query, got %q", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &Client{
		endpoint:   server.URL,
		httpClient: server.Client(),
	}

	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
}
