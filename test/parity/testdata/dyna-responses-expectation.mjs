// D192: on the Pi side of a paired probe, project the native OpenAI Responses default onto the captured result. Call only for requests that omit reasoningSummary.
export function expectNativeResponsesSummary(reasoning) {
  if (reasoning?.summary !== 'auto') {
    throw new Error(`Pi's native Responses default is no longer auto: ${JSON.stringify(reasoning)}`);
  }
  reasoning.summary = 'detailed';
}
