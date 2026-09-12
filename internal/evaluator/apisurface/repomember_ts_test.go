package apisurface

import (
	"strings"
	"testing"
)

// nestShapedRepo lays out the sources of run api-7425be21b83f608e66318fdd99528522's fixture: an
// OrdersService with getAllOrdersLegacy/listOrders/createOrder (the generated test invented
// `create`), a service hierarchy inside the repository, one that extends a package class, and one
// with an index signature.
func nestShapedRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeRepoJava(t, repo, "src/orders/orders.service.ts", `import { Injectable } from '@nestjs/common';
import { randomUUID } from 'crypto';
import type { CreateOrderDto } from './dto/create-order.dto';

export interface Order { id: string; sku: string; qty: number; }

@Injectable()
export class OrdersService {
  /** @deprecated */
  getAllOrdersLegacy(): Order[] {
    return this.listOrders();
  }

  listOrders(): Order[] {
    return [];
  }

  createOrder(dto: CreateOrderDto): Order {
    return { id: randomUUID(), sku: dto.sku, qty: dto.qty };
  }
}
`)
	writeRepoJava(t, repo, "src/common/base.service.ts", `export abstract class BaseService {
  protected readonly started = Date.now();
  ping(): boolean { return true; }
  get uptime(): number { return Date.now() - this.started; }
}
`)
	writeRepoJava(t, repo, "src/billing/billing-http.client.ts", `export class BillingHttpClient {
  async ping(url: string): Promise<void> {}
}
`)
	writeRepoJava(t, repo, "src/billing/billing.service.ts", `import { Injectable } from '@nestjs/common';
import { BaseService } from '../common/base.service';
import { BillingHttpClient } from './billing-http.client';

@Injectable()
export class BillingService extends BaseService {
  private preflightDone = false;
  readonly label = 'billing';
  onPreflight = (): void => {};

  constructor(private readonly http: BillingHttpClient, public readonly name: string) {
    super();
  }

  async runBillingPreflight(): Promise<void> {
    void this.http.ping('/ping');
  }

  static create(): BillingService { return new BillingService(new BillingHttpClient(), 'x'); }
}
`)
	writeRepoJava(t, repo, "src/ext/ext.service.ts", `import { EventEmitter } from 'events';
export class ExtService extends EventEmitter {
  fire(): void {}
}
`)
	writeRepoJava(t, repo, "src/legacy/dyn.service.ts", `export class DynService {
  [key: string]: any;
  known(): void {}
}
`)
	writeRepoJava(t, repo, "src/orders/orders.controller.ts", `import { OrdersService } from './orders.service';
export class OrdersController {
  constructor(private readonly ordersService: OrdersService) {}
  createOrder(dto: any) { return this.ordersService.createOrder(dto); }
}
`)
	return repo
}

const ordersTestHead = `import { Test } from '@nestjs/testing';
import { OrdersController } from './orders.controller';
import { OrdersService } from './orders.service';
`

func TestRepoInventedMemberReasonTS_typedLocalCall(t *testing.T) {
	repo := nestShapedRepo(t)
	content := ordersTestHead + `
describe('OrdersController', () => {
  let service: OrdersService;
  it('creates', async () => {
    const result = await service.create({ sku: 'a', qty: 1 });
    expect(service.createOrder).toBeDefined();
  });
});
`
	got := RepoInventedMemberReasonTS(repo, "src/orders/orders.controller.test.ts", content)
	if !strings.Contains(got, "create") || !strings.Contains(got, "OrdersService") || !strings.Contains(got, "src/orders/orders.service.ts") {
		t.Fatalf("reason = %q, want the invented member, its type and source", got)
	}
	// The declared members are what makes the single retry land.
	for _, m := range []string{"getAllOrdersLegacy", "listOrders", "createOrder"} {
		if !strings.Contains(got, m) {
			t.Errorf("reason %q does not list declared member %s", got, m)
		}
	}
	if strings.Contains(got, "createOrder()") && strings.Count(got, "createOrder") > 1 {
		t.Errorf("createOrder exists and must not be reported: %q", got)
	}
}

func TestRepoInventedMemberReasonTS_spyOnAndPrototype(t *testing.T) {
	repo := nestShapedRepo(t)
	content := ordersTestHead + `
describe('x', () => {
  let service: OrdersService;
  it('spies', () => {
    jest.spyOn(service, 'create').mockResolvedValue(undefined as any);
    jest.spyOn(OrdersService.prototype, 'listOrders');
    jest.spyOn(OrdersService.prototype, 'findAll');
  });
});
`
	got := RepoInventedMemberReasonTS(repo, "src/orders/orders.controller.test.ts", content)
	if !strings.Contains(got, "create") || !strings.Contains(got, "findAll") {
		t.Fatalf("reason = %q, want both spied names", got)
	}
	if strings.Contains(got, "listOrders()") && strings.Contains(got, "call to listOrders") {
		t.Errorf("listOrders exists: %q", got)
	}
}

func TestRepoInventedMemberReasonTS_moduleGetAttribution(t *testing.T) {
	repo := nestShapedRepo(t)
	for name, decl := range map[string]string{
		"generic get":    `const service = module.get<OrdersService>(OrdersService);`,
		"positional get": `const service = module.get(OrdersService);`,
		"resolve":        `const service = await moduleRef.resolve(OrdersService);`,
		"new":            `const service = new OrdersService();`,
	} {
		t.Run(name, func(t *testing.T) {
			content := ordersTestHead + "\nit('x', () => {\n  " + decl + "\n  service.create({});\n  service.listOrders();\n});\n"
			got := RepoInventedMemberReasonTS(repo, "src/orders/orders.controller.test.ts", content)
			if !strings.Contains(got, "create") {
				t.Fatalf("reason = %q, want create reported", got)
			}
			if strings.Contains(got, "listOrders") && !strings.Contains(got, "declares") {
				t.Errorf("listOrders is real: %q", got)
			}
		})
	}
}

// THE CASE tsc CANNOT SEE. Nest's provider `useValue` is typed any, so a mock that spells the
// method wrong compiles, and the controller fails at run time with "createOrder is not a function"
// — the second failure of run api-7425be21b83f608e66318fdd99528522.
func TestRepoInventedMemberReasonTS_providerUseValueLiteral(t *testing.T) {
	repo := nestShapedRepo(t)
	cases := map[string]string{
		"inline literal": `providers: [OrdersController, { provide: OrdersService, useValue: { create: jest.fn(), listOrders: jest.fn() } }],`,
		"identifier literal": `const mockOrdersService = {
  create: jest.fn().mockResolvedValue({ id: '1' }),
  listOrders: jest.fn(),
};
const providers = [{ provide: OrdersService, useValue: mockOrdersService }];`,
		"cast literal":       `const svc = { create: jest.fn() } as unknown as OrdersService;`,
		"mocked annotation":  `const svc: jest.Mocked<OrdersService> = { create: jest.fn() } as any;`,
		"partial annotation": `const svc: Partial<OrdersService> = { create: jest.fn() };`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			content := ordersTestHead + "\n" + body + "\n"
			got := RepoInventedMemberReasonTS(repo, "src/orders/orders.controller.test.ts", content)
			if !strings.Contains(got, "create") || !strings.Contains(got, "OrdersService") {
				t.Fatalf("reason = %q, want the mock's invented key", got)
			}
			if strings.Contains(got, "mock") && !strings.Contains(strings.ToLower(got), "mock") {
				t.Errorf("reason should say it is a mock shape: %q", got)
			}
		})
	}
}

func TestRepoInventedMemberReasonTS_mockLiteralWithRealKeysIsSilent(t *testing.T) {
	repo := nestShapedRepo(t)
	content := ordersTestHead + `
const mockOrdersService = { createOrder: jest.fn(), listOrders: jest.fn() };
const providers = [{ provide: OrdersService, useValue: mockOrdersService }];
it('x', () => { mockOrdersService.createOrder.mockReturnValue({}); });
`
	if got := RepoInventedMemberReasonTS(repo, "src/orders/orders.controller.test.ts", content); got != "" {
		t.Fatalf("real keys must be silent, got %q", got)
	}
}

// A mock typed as the class: the chained access on an invented key is the invented member.
func TestRepoInventedMemberReasonTS_chainedAccessOnTypedMock(t *testing.T) {
	repo := nestShapedRepo(t)
	content := ordersTestHead + `
let svc: jest.Mocked<OrdersService>;
it('x', () => {
  svc.create.mockResolvedValue({} as any);
  svc.createOrder.mockReturnValue({} as any);
});
`
	got := RepoInventedMemberReasonTS(repo, "src/orders/orders.controller.test.ts", content)
	if !strings.Contains(got, "create") {
		t.Fatalf("reason = %q, want create", got)
	}
}

func TestRepoInventedMemberReasonTS_hierarchyInsideRepo(t *testing.T) {
	repo := nestShapedRepo(t)
	content := `import { BillingService } from './billing.service';
let billing: BillingService;
it('x', async () => {
  billing.ping();
  billing.http.ping('/x');
  billing.name.toUpperCase();
  billing.label.length;
  billing.onPreflight();
  billing.uptime;
  await billing.runBillingPreflight();
  billing.charge();
});
`
	got := RepoInventedMemberReasonTS(repo, "src/billing/billing.service.test.ts", content)
	if !strings.Contains(got, "charge") {
		t.Fatalf("reason = %q, want charge reported", got)
	}
	for _, real := range []string{"ping", "http", "name", "label", "onPreflight", "uptime", "runBillingPreflight"} {
		if strings.Contains(got, "call to "+real) || strings.Contains(got, "access to "+real) {
			t.Errorf("%s is declared (own, inherited or constructor property): %q", real, got)
		}
	}
}

func TestRepoInventedMemberReasonTS_silentWhenUnprovable(t *testing.T) {
	repo := nestShapedRepo(t)
	cases := map[string]struct{ path, content string }{
		"extends a package class": {"src/ext/ext.service.test.ts", `import { ExtService } from './ext.service';
let s: ExtService; it('x', () => { s.emit('x'); s.nonsense(); });
`},
		"index signature": {"src/legacy/dyn.service.test.ts", `import { DynService } from './dyn.service';
let s: DynService; it('x', () => { s.anything(); });
`},
		"type imported from a package": {"src/orders/x.test.ts", `import { Repository } from 'typeorm';
let r: Repository; it('x', () => { r.whatever(); });
`},
		"shadowed by a local class": {"src/orders/orders.controller.test.ts", ordersTestHead + `
class OrdersService { create() {} }
let s: OrdersService; it('x', () => { s.create(); });
`},
		"local declared with two types": {"src/orders/orders.controller.test.ts", ordersTestHead + `
let s: OrdersService;
let s: OrdersController;
it('x', () => { s.create(); });
`},
		"object prototype and constructor": {"src/orders/orders.controller.test.ts", ordersTestHead + `
let s: OrdersService; it('x', () => { s.toString(); s.constructor.name; s.hasOwnProperty('x'); });
`},
		"spread in the mock literal": {"src/orders/orders.controller.test.ts", ordersTestHead + `
const base = {};
const providers = [{ provide: OrdersService, useValue: { ...base, create: jest.fn() } }];
`},
		"string and comment contents": {"src/orders/orders.controller.test.ts", ordersTestHead + `
let s: OrdersService;
// s.create() is not a member
const msg = "s.create()";
it('x', () => { s.listOrders(); });
`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := RepoInventedMemberReasonTS(repo, tc.path, tc.content); got != "" {
				t.Fatalf("want silence, got %q", got)
			}
		})
	}
}

// An extend payload carries no imports of its own; the target file on disk has them.
func TestRepoInventedMemberReasonTS_extendPayloadResolvesImportsFromTarget(t *testing.T) {
	repo := nestShapedRepo(t)
	writeRepoJava(t, repo, "src/orders/orders.service.test.ts", ordersTestHead+`
describe('OrdersService', () => {
  let service: OrdersService;
  it('lists', () => { expect(service.listOrders()).toEqual([]); });
});
`)
	payload := `  it('creates', () => {
    const service = new OrdersService();
    expect(service.create({ sku: 'a', qty: 1 })).toBeDefined();
  });
`
	got := RepoInventedMemberReasonTS(repo, "src/orders/orders.service.test.ts", payload)
	if !strings.Contains(got, "create") {
		t.Fatalf("reason = %q, want create resolved through the target's imports", got)
	}
}

func TestRepoInventedMemberReasonTS_javascriptRequire(t *testing.T) {
	repo := nestShapedRepo(t)
	writeRepoJava(t, repo, "lib/cart.js", `class Cart {
  add(item) { this.items.push(item); }
  total() { return 0; }
}
module.exports = { Cart };
`)
	content := `const { Cart } = require('./cart');
test('x', () => {
  const cart = new Cart();
  cart.add(1);
  cart.clear();
});
`
	got := RepoInventedMemberReasonTS(repo, "lib/cart.test.js", content)
	if !strings.Contains(got, "clear") || strings.Contains(got, "call to add") {
		t.Fatalf("reason = %q", got)
	}
}

func TestRepoInventedMemberReasonTS_emptyInputs(t *testing.T) {
	repo := nestShapedRepo(t)
	if got := RepoInventedMemberReasonTS("", "src/x.test.ts", "let s: OrdersService; s.create();"); got != "" {
		t.Errorf("no repo root: %q", got)
	}
	if got := RepoInventedMemberReasonTS(repo, "", ordersTestHead+"let s: OrdersService; s.create();"); got != "" {
		t.Errorf("no test path, relative imports cannot resolve: %q", got)
	}
	if got := RepoInventedMemberReasonTS(repo, "src/orders/x.test.ts", "  "); got != "" {
		t.Errorf("empty content: %q", got)
	}
}
