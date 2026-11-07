interface Person {
  name: string;
  age: number;
}

export function greet(p: Person): string {
  const line = `Hello, ${p.name}`;
  return line + (p.age > 1 ? '!' : '.');
}

const who: Person = { name: 'Ana', age: 30 };
const said = greet(who);
console.log(said);
