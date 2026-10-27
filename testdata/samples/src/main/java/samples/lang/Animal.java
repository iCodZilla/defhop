package samples.lang;

/** Base type for inheritance / override navigation. */
public class Animal {
    protected String name;

    public Animal(String name) {
        this.name = name;
    }

    public String speak() {
        return name;
    }

    public String getName() {
        return name;
    }
}
