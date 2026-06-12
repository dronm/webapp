package webapp

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/dronm/modelbind"
)

func TestQueryAndPathInputBinderPathValueOverridesQueryValue(t *testing.T) {
	binder := QueryAndPathInputBinder[*testInputModel](false, "id")
	req := httptest.NewRequest("GET", "/supplier/10?id=20&name=Alice", nil)
	req.SetPathValue("id", "10")

	got, err := binder(req)
	if err != nil {
		t.Fatalf("binder() error = %v", err)
	}

	input, ok := got.(modelbind.ModelInput[*testInputModel])
	if !ok {
		t.Fatalf("input type = %T, want modelbind.ModelInput[*testInputModel]", got)
	}
	if input.Model.ID == nil || *input.Model.ID != 10 {
		t.Fatalf("id = %#v, want 10", input.Model.ID)
	}
	if input.Model.Name == nil || *input.Model.Name != "Alice" {
		t.Fatalf("name = %#v, want Alice", input.Model.Name)
	}
}

func TestQueryValueBinderParsesBoolAliases(t *testing.T) {
	binder := QueryValueBinder[bool]("active")
	req := httptest.NewRequest("GET", "/supplier/list?active=on", nil)

	got, err := binder(req)
	if err != nil {
		t.Fatalf("binder() error = %v", err)
	}
	if got != true {
		t.Fatalf("got = %#v, want true", got)
	}
}

func TestPathValueBinderParsesPointers(t *testing.T) {
	binder := PathValueBinder[*int]("id")
	req := httptest.NewRequest("GET", "/supplier/15", nil)
	req.SetPathValue("id", "15")

	got, err := binder(req)
	if err != nil {
		t.Fatalf("binder() error = %v", err)
	}

	id, ok := got.(*int)
	if !ok {
		t.Fatalf("got type = %T, want *int", got)
	}
	if id == nil || *id != 15 {
		t.Fatalf("id = %#v, want 15", id)
	}
}

func TestParseCollectionParamsFromIndividualQueryValues(t *testing.T) {
	filter := url.QueryEscape(`[{"j":"and","f":{"name":{"o":"ilk","v":"alice"}}}]`)
	sorter := url.QueryEscape(`[{"f":"name","d":"a"}]`)
	req := httptest.NewRequest("GET", "/supplier/list?from=5&count=20&filter="+filter+"&sorter="+sorter, nil)

	params, err := ParseCollectionParams(req)
	if err != nil {
		t.Fatalf("ParseCollectionParams() error = %v", err)
	}
	if params.From != modelbind.CollectionFrom(5) {
		t.Fatalf("from = %d, want 5", params.From)
	}
	if params.Count != modelbind.CollectionCount(20) {
		t.Fatalf("count = %d, want 20", params.Count)
	}
	if len(params.Filter) != 1 {
		t.Fatalf("filter length = %d, want 1", len(params.Filter))
	}
	if len(params.Sorter) != 1 {
		t.Fatalf("sorter length = %d, want 1", len(params.Sorter))
	}
}

func TestParseCollectionParamsFromParamsObject(t *testing.T) {
	raw := url.QueryEscape(`{"from":3,"count":7,"sorter":[{"f":"name","d":"d"}]}`)
	req := httptest.NewRequest("GET", "/supplier/list?params="+raw, nil)

	params, err := ParseCollectionParams(req)
	if err != nil {
		t.Fatalf("ParseCollectionParams() error = %v", err)
	}
	if params.From != modelbind.CollectionFrom(3) {
		t.Fatalf("from = %d, want 3", params.From)
	}
	if params.Count != modelbind.CollectionCount(7) {
		t.Fatalf("count = %d, want 7", params.Count)
	}
	if len(params.Sorter) != 1 || params.Sorter[0].Field != "name" || params.Sorter[0].Direct != modelbind.SortParDesc {
		t.Fatalf("sorter = %#v, want name desc", params.Sorter)
	}
}
