"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.greet = greet;
function greet(p) {
    const line = `Hello, ${p.name}`;
    return line + (p.age > 1 ? '!' : '.');
}
const who = { name: 'Ana', age: 30 };
const said = greet(who);
console.log(said);
//# sourceMappingURL=greet.js.map