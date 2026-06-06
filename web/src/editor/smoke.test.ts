// Browser-only smoke test for the Lit harness.
// Proves that: render() mounts a custom element, its shadow DOM is inspectable,
// and eq() passes — end-to-end through headless Chromium.
//
// Run via: npm run test:web  (from web/)

import {LitElement, html} from "lit";
import type {T} from "../../testing/harness.ts";
import {eq, render, serialize} from "../../testing/harness.ts";

// Minimal Lit element using the non-decorator API (erasable-syntax-only).
class SmokeEl extends LitElement {
    static override properties = {name: {type: String}};
    declare name: string;
    constructor() {
        super();
        this.name = "world";
    }
    protected override render() {
        return html`<p>hello ${this.name}</p>`;
    }
}
customElements.define("x-smoke", SmokeEl);

export async function testRenders(t: T): Promise<void> {
    const el = await render(html`<x-smoke></x-smoke>`);
    eq(serialize(el), "<p>hello world</p>");
}
