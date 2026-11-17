import { describe, expect, it } from "vitest";
import { parse } from "csv-parse/browser/esm";

// CsvViewer.vue parses with the browser ESM build of csv-parse and these
// options; this pins the behaviour across csv-parse majors.
function parseLikeCsvViewer(content: string, delimiter: string[]) {
  return new Promise<string[][]>((resolve, reject) => {
    parse(
      content,
      { delimiter, skip_empty_lines: true, relax_column_count: true },
      (error, output) => (error ? reject(error) : resolve(output))
    );
  });
}

describe("csv-parse (browser ESM build)", () => {
  it("accepts several delimiters at once", async () => {
    const rows = await parseLikeCsvViewer("a,b;c\n1;2,3\n", [",", ";"]);
    expect(rows).toEqual([
      ["a", "b", "c"],
      ["1", "2", "3"],
    ]);
  });

  it("skips empty lines and tolerates ragged rows", async () => {
    const rows = await parseLikeCsvViewer("h1,h2\n\n1\n1,2,3\n", [","]);
    expect(rows).toEqual([["h1", "h2"], ["1"], ["1", "2", "3"]]);
  });

  it("reports malformed input through the callback", async () => {
    await expect(parseLikeCsvViewer('a,"b\n', [","])).rejects.toThrow();
  });
});
