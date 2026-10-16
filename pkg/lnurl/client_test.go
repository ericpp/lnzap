package lnurl

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestAddPayerName(t *testing.T) {
	raw := []byte(`{"callback":"https://up.example/pay","minSendable":1000,"metadata":"[[\"text/plain\",\"hi\"]]","tag":"payRequest"}`)
	out, err := AddPayerName(raw)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	if body["metadata"] != `[["text/plain","hi"]]` {
		t.Fatalf("metadata %#v", body["metadata"])
	}
	if body["minSendable"].(float64) != 1000 {
		t.Fatalf("minSendable %#v", body["minSendable"])
	}
	payerData, _ := body["payerData"].(map[string]any)
	name, _ := payerData["name"].(map[string]any)
	if name["mandatory"] != false {
		t.Fatalf("payerData %#v", body["payerData"])
	}
	if !bytes.Contains(out, []byte(`"minSendable":1000`)) {
		t.Fatalf("number rewritten: %s", out)
	}
}

func TestAddPayerNameLeavesExistingName(t *testing.T) {
	raw := []byte(`{"payerData":{"email":{"mandatory":false},"name":{"mandatory":true}},"minSendable":1000}`)
	out, err := AddPayerName(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("changed %s", out)
	}
}

func TestAddPayerNameKeepsOtherFields(t *testing.T) {
	raw := []byte(`{"payerData":{"email":{"mandatory":false}}}`)
	out, err := AddPayerName(raw)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	payerData, _ := body["payerData"].(map[string]any)
	email, _ := payerData["email"].(map[string]any)
	name, _ := payerData["name"].(map[string]any)
	if email["mandatory"] != false || name["mandatory"] != false {
		t.Fatalf("payerData %#v", body["payerData"])
	}
}
