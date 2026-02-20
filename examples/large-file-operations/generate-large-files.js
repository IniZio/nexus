const fs = require('fs');
const path = require('path');

const OUTPUT_DIR = path.join(__dirname, 'generated');

function ensureDir() {
  if (!fs.existsSync(OUTPUT_DIR)) {
    fs.mkdirSync(OUTPUT_DIR, { recursive: true });
  }
}

function generateLargeJSON(filename, itemCount) {
  const items = [];
  for (let i = 0; i < itemCount; i++) {
    items.push({
      id: i,
      name: `Item ${i}`,
      description: `This is a detailed description for item ${i} with lots of text content.`.repeat(5),
      timestamp: new Date().toISOString(),
      metadata: {
        version: 1,
        source: 'generator',
        tags: ['tag1', 'tag2', 'tag3']
      },
      nested: {
        level1: {
          level2: {
            level3: {
              value: `Deep nested value ${i}`
            }
          }
        }
      }
    });
  }

  const content = JSON.stringify(items, null, 2);
  const filepath = path.join(OUTPUT_DIR, filename);
  fs.writeFileSync(filepath, content);
  const stats = fs.statSync(filepath);
  console.log(`Generated ${filename}: ${(stats.size / 1024 / 1024).toFixed(2)} MB (${itemCount} items)`);
  return filepath;
}

function generateLargeCSV(filename, rowCount) {
  const headers = ['id', 'name', 'email', 'phone', 'address', 'city', 'country', 'created_at'];
  const lines = [headers.join(',')];

  const domains = ['example.com', 'test.org', 'demo.net'];
  const cities = ['New York', 'Los Angeles', 'Chicago', 'Houston', 'Phoenix'];

  for (let i = 0; i < rowCount; i++) {
    const row = [
      i,
      `User ${i}`,
      `user${i}@${domains[i % domains.length]}`,
      `555-${String(i % 1000).padStart(3, '0')}-${String(i % 10000).padStart(4, '0')}`,
      `${i} Main Street`,
      cities[i % cities.length],
      'USA',
      new Date().toISOString()
    ];
    lines.push(row.map(field => `"${field}"`).join(','));
  }

  const content = lines.join('\n');
  const filepath = path.join(OUTPUT_DIR, filename);
  fs.writeFileSync(filepath, content);
  const stats = fs.statSync(filepath);
  console.log(`Generated ${filename}: ${(stats.size / 1024 / 1024).toFixed(2)} MB (${rowCount} rows)`);
  return filepath;
}

function generateLargeText(filename, lineCount) {
  const lines = [];
  const lorem = 'Lorem ipsum dolor sit amet, consectetur adipiscing elit. '.repeat(10);

  for (let i = 0; i < lineCount; i++) {
    lines.push(`Line ${i + 1}: ${lorem}`);
  }

  const content = lines.join('\n');
  const filepath = path.join(OUTPUT_DIR, filename);
  fs.writeFileSync(filepath, content);
  const stats = fs.statSync(filepath);
  console.log(`Generated ${filename}: ${(stats.size / 1024 / 1024).toFixed(2)} MB (${lineCount} lines)`);
  return filepath;
}

function generateManyFiles(dirname, fileCount, fileSizeKB) {
  const dir = path.join(OUTPUT_DIR, dirname);
  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true });
  }

  for (let i = 0; i < fileCount; i++) {
    const content = 'x'.repeat(fileSizeKB * 1024);
    const filepath = path.join(dir, `file_${String(i).padStart(4, '0')}.txt`);
    fs.writeFileSync(filepath, content);
  }

  console.log(`Generated ${fileCount} files of ${fileSizeKB} KB each in ${dirname}/`);
}

async function main() {
  console.log('Generating large files for Nexus performance testing...\n');
  ensureDir();

  console.log('=== Small Files (KB range) ===');
  generateLargeJSON('small.json', 100);
  generateLargeCSV('small.csv', 1000);
  generateLargeText('small.txt', 1000);

  console.log('\n=== Medium Files (MB range) ===');
  generateLargeJSON('medium.json', 10000);
  generateLargeCSV('medium.csv', 100000);
  generateLargeText('medium.txt', 50000);

  console.log('\n=== Large Files (10+ MB range) ===');
  generateLargeJSON('large.json', 100000);
  generateLargeCSV('large.csv', 500000);
  generateLargeText('large.txt', 200000);

  console.log('\n=== Many Small Files ===');
  generateManyFiles('many_small', 100, 10);
  generateManyFiles('many_medium', 50, 100);

  console.log('\n=== Summary ===');
  let totalSize = 0;
  const walkDir = (dir) => {
    const files = fs.readdirSync(dir);
    for (const file of files) {
      const filepath = path.join(dir, file);
      const stats = fs.statSync(filepath);
      if (stats.isDirectory()) {
        walkDir(filepath);
      } else {
        totalSize += stats.size;
      }
    }
  };
  walkDir(OUTPUT_DIR);
  console.log(`Total generated: ${(totalSize / 1024 / 1024).toFixed(2)} MB`);
}

main().catch(console.error);
