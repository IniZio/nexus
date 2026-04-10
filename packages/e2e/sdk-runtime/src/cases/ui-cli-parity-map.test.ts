import matrix from '../parity/matrix.json';
import { implementedCaseIds } from './test-ids';

type MatrixShape = {
  ids: string[];
};

describe('ui/cli parity map', () => {
  it('all parity matrix test IDs are implemented by sdk-runtime cases', () => {
    const implemented = new Set<string>(implementedCaseIds);

    const parsed = matrix as MatrixShape;
    const missing = parsed.ids.filter((id) => !implemented.has(id));

    expect(parsed.ids.length).toBeGreaterThan(0);
    expect(missing).toEqual([]);
  });
});
