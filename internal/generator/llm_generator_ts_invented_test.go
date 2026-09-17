package generator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// messageCapturingCompleter answers from a script and keeps every user message it was sent.
type messageCapturingCompleter struct {
	replies []string
	users   []string
}

func (c *messageCapturingCompleter) Complete(_ context.Context, msgs []model.Message, _ model.CompleteOptions) (*model.CompleteResult, error) {
	for _, m := range msgs {
		if m.Role == "user" {
			c.users = append(c.users, m.Content)
		}
	}
	i := len(c.users) - 1
	if i >= len(c.replies) {
		i = len(c.replies) - 1
	}
	return &model.CompleteResult{Content: c.replies[i]}, nil
}

func nestRepoForGenerator(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for rel, body := range map[string]string{
		"src/orders/orders.service.ts":    "export class OrdersService {\n  listOrders() { return []; }\n  createOrder(dto: any) { return dto; }\n}\n",
		"src/orders/orders.controller.ts": "import { OrdersService } from './orders.service';\nexport class OrdersController {\n  constructor(private readonly ordersService: OrdersService) {}\n  createOrder(dto: any) { return this.ordersService.createOrder(dto); }\n}\n",
	} {
		full := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func tsControllerItem() *retrieval.TestPlanItem {
	return &retrieval.TestPlanItem{
		Layer: "unit",
		Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{
			Kind: "METHOD", Lang: "typescript",
			File:   "src/orders/orders.controller.ts",
			FQName: "src.orders.orders.controller.OrdersController.createOrder",
		}},
	}
}

const tsInventedTest = "import { OrdersService } from './orders.service';\n" +
	"describe('OrdersController', () => {\n  let service: OrdersService;\n" +
	"  const providers = [{ provide: OrdersService, useValue: { create: jest.fn() } }];\n" +
	"  it('creates', () => { expect(service.create({})).toBeDefined(); });\n});\n"

const tsCorrectTest = "import { OrdersService } from './orders.service';\n" +
	"describe('OrdersController', () => {\n  let service: OrdersService;\n" +
	"  const providers = [{ provide: OrdersService, useValue: { createOrder: jest.fn() } }];\n" +
	"  it('creates', () => { expect(service.createOrder({})).toBeDefined(); });\n});\n"

// Run api-7425be21b83f608e66318fdd99528522: the controller test invented OrdersService.create in
// a typed call and in a provider mock. The typed call cost a static-gate repair round, the mock a
// fixer round at run time. Both are now named before the file is written, and one retry fixes them.
func TestLLMGenerator_Generate_retriesOnInventedTypeScriptMember(t *testing.T) {
	repo := nestRepoForGenerator(t)
	c := &messageCapturingCompleter{replies: []string{tsInventedTest, tsCorrectTest}}
	aud := &recordingAuditor{}
	g := &LLMGenerator{LLM: c, RepoPath: repo, TestFramework: "jest", DisableStructuredGenerateOutput: true, Audit: aud}

	got, path, err := g.Generate(context.Background(), tsControllerItem(), "ctx")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.users) != 2 {
		t.Fatalf("completions = %d (path %q), want the first pass and one API retry", len(c.users), path)
	}
	retry := c.users[1]
	for _, want := range []string{"API retry", "create", "OrdersService declares: listOrders, createOrder", "mock for OrdersService"} {
		if !strings.Contains(retry, want) {
			t.Errorf("retry prompt lacks %q:\n%s", want, retry)
		}
	}
	if strings.TrimSpace(got) != strings.TrimSpace(tsCorrectTest) {
		t.Errorf("content = %q, want the corrected retry output", got)
	}
	found := false
	for _, s := range aud.steps {
		if s == "generate.invented_member_rejected" {
			found = true
		}
	}
	if !found {
		t.Errorf("rejection not audited: %v", aud.steps)
	}
}

func TestLLMGenerator_Generate_noRetryWhenTypeScriptMembersAreReal(t *testing.T) {
	repo := nestRepoForGenerator(t)
	c := &messageCapturingCompleter{replies: []string{tsCorrectTest}}
	g := &LLMGenerator{LLM: c, RepoPath: repo, TestFramework: "jest", DisableStructuredGenerateOutput: true}

	if _, _, err := g.Generate(context.Background(), tsControllerItem(), "ctx"); err != nil {
		t.Fatal(err)
	}
	if len(c.users) != 1 {
		t.Errorf("completions = %d, want exactly one", len(c.users))
	}
}
