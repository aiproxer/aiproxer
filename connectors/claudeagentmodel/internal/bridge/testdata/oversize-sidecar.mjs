process.stdout.write(
  JSON.stringify({
    kind: 'hello',
    protocol: '1',
    sdk_version: '0.3.316',
    node_version: process.version,
  }) + '\n',
)
process.stdout.write('x'.repeat(9 * 1024 * 1024) + '\n')
setTimeout(() => process.exit(0), 200)
