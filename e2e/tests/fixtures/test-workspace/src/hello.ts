export function hello(name: string): string {
  return `Hello, ${name}!`;
}

export class Greeter {
  private readonly greeting: string;

  constructor(greeting: string) {
    this.greeting = greeting;
  }

  greet(): string {
    return this.greeting;
  }
}

if (require.main === module) {
  console.log(hello('World'));
  const greeter = new Greeter('Hello');
  console.log(greeter.greet());
}
