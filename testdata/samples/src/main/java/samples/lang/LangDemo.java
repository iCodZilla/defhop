package samples.lang;

/** Entry points that exercise type / method / field jumps. */
public class LangDemo {
    private final Greeter greeter = new FriendlyGreeter();
    private final Overloads overloads = new Overloads();

    public String run(Dog dog) {
        Animal animal = dog;
        Box<String> box = new Box<>(animal.speak());
        int n = overloads.add(1, 2);
        Status st = Status.ACTIVE;
        return greeter.greet(box.getValue()) + n + st.name();
    }
}
