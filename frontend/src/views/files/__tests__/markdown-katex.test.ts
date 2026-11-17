import { describe, expect, it } from "vitest";
import { Marked } from "marked";
import markedKatex from "marked-katex-extension";

// Mirrors the markdown preview setup in Editor.vue (marked + KaTeX rendered
// as MathML, errors rendered inline instead of thrown).
function render(markdown: string) {
  const marked = new Marked();
  marked.use(markedKatex({ output: "mathml", throwOnError: false }));
  return marked.parse(markdown, { async: true });
}

describe("markdown preview with KaTeX", () => {
  it("renders inline math as MathML", async () => {
    const html = await render("Euler: $e^{i\\pi} + 1 = 0$");
    expect(html).toContain("<math");
    expect(html).toContain("<msup>");
    expect(html).not.toContain("$e^");
  });

  it("renders display math as a block", async () => {
    const html = await render("$$\n\\frac{a}{b}\n$$");
    expect(html).toContain('display="block"');
    expect(html).toContain("<mfrac>");
  });

  it("does not throw on invalid TeX", async () => {
    const html = await render("$\\notacommand{x}$");
    expect(html).toContain("notacommand");
  });
});
